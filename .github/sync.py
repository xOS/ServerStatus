import os
import time
import requests
import hashlib
from github import Github, Auth
import socket
from typing import List


REQUEST_TIMEOUT = (10, 300)  # (connect timeout, read timeout)
# 上传可能较慢，单独设置更长的读超时
UPLOAD_TIMEOUT = (10, 900)
MAX_DOWNLOAD_RETRIES = 3
MAX_UPLOAD_RETRIES = 5
UPLOAD_SOCKET_TIMEOUT = 900


def get_github_latest_release():
    # 使用令牌提升速率配额
    gh_token = os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')
    if gh_token:
        g = Github(auth=Auth.Token(gh_token))
    else:
        g = Github()
    repo = g.get_repo("xOS/ServerStatus")
    release = repo.get_latest_release()
    if release:
        print(f"Latest release tag is: {release.tag_name}")
        print(f"Latest release info is: {release.body}")
        files = []
        asset_links = {}
        for asset in release.get_assets():
            url = asset.browser_download_url
            name = asset.name

            # 下载资产（流式 + 超时 + 有限重试）
            for attempt in range(1, MAX_DOWNLOAD_RETRIES + 1):
                try:
                    with requests.get(url, stream=True, timeout=REQUEST_TIMEOUT) as r:
                        if r.status_code == 200:
                            with open(name, 'wb') as f:
                                for chunk in r.iter_content(chunk_size=1024 * 256):
                                    if chunk:
                                        f.write(chunk)
                            print(f"Downloaded {name}")
                            break
                        else:
                            print(f"Failed to download {name}, status: {r.status_code}, attempt {attempt}/{MAX_DOWNLOAD_RETRIES}")
                except requests.RequestException as e:
                    print(f"Download error for {name}: {e} (attempt {attempt}/{MAX_DOWNLOAD_RETRIES})")
                time.sleep(2 ** (attempt - 1))
            else:
                raise RuntimeError(f"Failed to download {name} after {MAX_DOWNLOAD_RETRIES} attempts")
            file_abs_path = get_abs_path(asset.name)
            files.append(file_abs_path)
            asset_links[name] = url
        sync_to_gitee(release.tag_name, release.body, files, asset_links)
    else:
        print("No releases found.")


def delete_gitee_releases(latest_id, client, uri, token):
    # 仅当 latest_id 有效时执行保留最新
    if not latest_id:
        print('Skip delete_gitee_releases: latest_id is empty')
        return

    params = {'access_token': token, 'page': 1, 'per_page': 100}
    release_info = []
    release_response = client.get(uri, params=params, timeout=REQUEST_TIMEOUT)
    if release_response.status_code == 200:
        release_info = release_response.json()
    else:
        print(f"List releases failed: {release_response.status_code} {release_response.text}")
        return

    release_ids = []
    for block in release_info:
        if 'id' in block:
            release_ids.append(block['id'])

    print(f'Current release ids: {release_ids}')
    if latest_id in release_ids:
        release_ids.remove(latest_id)

    for rid in release_ids:
        release_uri = f"{uri}/{rid}"
        delete_response = client.delete(release_uri, params={'access_token': token}, timeout=REQUEST_TIMEOUT)
        if delete_response.status_code == 204:
            print(f'Successfully deleted release #{rid}.')
        else:
            raise ValueError(f"Delete release #{rid} failed: {delete_response.status_code} {delete_response.text}")


def sync_to_gitee(tag: str, body: str, files: slice, asset_links=None):
    release_id = ""
    owner = os.environ.get('GITEE_OWNER', 'Ten')
    repo = os.environ.get('GITEE_REPO', 'ServerStatus')
    release_api_uri = f"https://gitee.com/api/v5/repos/{owner}/{repo}/releases"
    api_client = requests.Session()
    api_client.headers.update({
        'Accept': 'application/json',
        # 对于 form 提交不强制指定 JSON 头
    })

    access_token = os.environ['GITEE_TOKEN']
    release_form = {
        'access_token': access_token,
        'tag_name': tag,
        'name': tag,
        'body': body,
        'prerelease': 'false',
        'target_commitish': 'master'
    }
    # 优先尝试创建（表单提交）
    release_api_response = api_client.post(release_api_uri, data=release_form, timeout=REQUEST_TIMEOUT)
    if release_api_response.status_code == 201:
        release_info = release_api_response.json()
        release_id = release_info.get('id')
    else:
        print(f"Create release failed: {release_api_response.status_code} {release_api_response.text}")
        # 如果已存在同名 tag，则尝试查找已存在的发布并复用其 id
        list_resp = api_client.get(release_api_uri, params={'access_token': access_token, 'page': 1, 'per_page': 100}, timeout=REQUEST_TIMEOUT)
        if list_resp.status_code == 200:
            for rel in list_resp.json():
                if rel.get('tag_name') == tag:
                    release_id = rel.get('id')
                    print(f"Found existing Gitee release id: {release_id} for tag {tag}")
                    break
        else:
            print(f"List releases failed: {list_resp.status_code} {list_resp.text}")

    print(f"Gitee release id: {release_id}")
    if not release_id:
        raise RuntimeError(f"No Gitee release id available for tag {tag}. Please ensure repo {owner}/{repo} exists and token has permission.")
    asset_api_uri = f"{release_api_uri}/{release_id}/attach_files"
    # 列出现有资产，避免重复上传导致 400/405
    existing_assets = {}
    try:
        # 获取单个 release 详情，其中包含 assets 列表
        release_detail_resp = api_client.get(f"{release_api_uri}/{release_id}", params={'access_token': access_token}, timeout=REQUEST_TIMEOUT)
        if release_detail_resp.status_code == 200:
            data = release_detail_resp.json()
            assets = data.get('assets') or []
            for a in assets:
                name = a.get('name')
                if name:
                    existing_assets[name] = a.get('id')
            print(f"Existing assets: {list(existing_assets.keys())}")
        else:
            print(f"Get release detail failed: {release_detail_resp.status_code} {release_detail_resp.text}")
    except requests.RequestException as e:
        print(f"List assets error: {e}")

    # 上传前按照文件大小从小到大排序，先传小文件以验证链路
    try:
        files = sorted(files, key=lambda p: os.path.getsize(p))
    except Exception:
        pass

    # 附件上传回退开关与出错是否继续
    allow_partial = os.environ.get('SYNC_CONTINUE_ON_UPLOAD_ERROR', 'false').lower() == 'true'
    # 链接模式：跳过上传，直接把 GitHub 资产链接写入 release body
    link_only = os.environ.get('SYNC_LINK_ONLY', 'false').lower() == 'true'
    try:
        link_threshold_mb = float(os.environ.get('SYNC_LINK_THRESHOLD_MB', '16'))
    except ValueError:
        link_threshold_mb = 16.0

    if asset_links is None:
        asset_links = {}

    body_additions = []

    for file_path in files:
        file_name = os.path.basename(file_path)
        try:
            size_bytes = os.path.getsize(file_path)
            print(f"Preparing to upload {file_name} ({size_bytes/1024/1024:.2f} MiB)")
        except OSError:
            pass
        success = False
        for attempt in range(1, MAX_UPLOAD_RETRIES + 1):
            # 若启用链接模式或超过阈值，直接跳过上传改为追加链接
            try:
                size_bytes = os.path.getsize(file_path)
            except OSError:
                size_bytes = 0
            if link_only or (link_threshold_mb > 0 and size_bytes >= link_threshold_mb * 1024 * 1024):
                url = asset_links.get(file_name)
                if url:
                    line = f"- {file_name}: {url}"
                    if line not in body_additions:
                        body_additions.append(line)
                    print(f"Link-only mode: skip upload and add link for {file_name}")
                    success = True
                    break
                else:
                    print(f"Link-only mode requested but no URL for {file_name}, will attempt upload.")
            try:
                # 如果已存在同名资产，则跳过上传
                if file_name in existing_assets:
                    print(f"Skip upload {file_name}: already exists in release (asset id {existing_assets[file_name]})")
                    success = True
                    break
                prev_sock_timeout = socket.getdefaulttimeout()
                socket.setdefaulttimeout(UPLOAD_SOCKET_TIMEOUT)
                try:
                    with open(file_path, 'rb') as fh:
                        # 第一次尝试：token 在 data，带 Expect 以优化首包
                        resp = api_client.post(
                            asset_api_uri,
                            data={'access_token': access_token},
                            files={'file': (file_name, fh, 'application/octet-stream')},
                            headers={'Expect': '100-continue'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                finally:
                    socket.setdefaulttimeout(prev_sock_timeout)
                if resp.status_code == 201:
                    asset_info = resp.json()
                    asset_name = asset_info.get('name')
                    print(f"Successfully uploaded {asset_name}!")
                    success = True
                    break
                else:
                    # 某些情况下返回400且文案提示已存在，视为成功
                    txt = resp.text
                    if resp.status_code in (400, 409, 422) and ('已存在' in txt or 'already exists' in txt):
                        print(f"Asset {file_name} already exists, treat as success.")
                        success = True
                        break
                    print(f"Upload failed (status {resp.status_code}) for {file_path}, attempt {attempt}/{MAX_UPLOAD_RETRIES}. Body: {txt[:256]}")
                    # 第二通道重试（不计入attempt），更换 token 位置与取消 Expect 头
                    try:
                        with open(file_path, 'rb') as fh:
                            resp2 = api_client.post(
                                asset_api_uri,
                                params={'access_token': access_token},
                                files={'file': (file_name, fh, 'application/octet-stream')},
                                timeout=UPLOAD_TIMEOUT,
                            )
                        if resp2.status_code == 201:
                            asset_info = resp2.json()
                            asset_name = asset_info.get('name')
                            print(f"Successfully uploaded via alt path: {asset_name}!")
                            success = True
                            break
                        else:
                            txt2 = resp2.text
                            if resp2.status_code in (400, 409, 422) and ('已存在' in txt2 or 'already exists' in txt2):
                                print(f"Asset {file_name} already exists (alt), treat as success.")
                                success = True
                                break
                            print(f"Alt upload failed (status {resp2.status_code}) for {file_path}. Body: {txt2[:256]}")
                    except requests.RequestException as e2:
                        print(f"Alt upload error for {file_path}: {e2}")
            except requests.RequestException as e:
                print(f"Upload error for {file_path}: {e} (attempt {attempt}/{MAX_UPLOAD_RETRIES})")
            time.sleep(min(60, 2 ** (attempt - 1)))
        if not success:
            print(f"Primary upload failed for {file_name} after {MAX_UPLOAD_RETRIES} attempts, trying attachments fallback...")
            # 回退：通过 attachments 接口上传，并把链接收集，末尾统一追加到 release body
            attach_uri = f"https://gitee.com/api/v5/repos/{owner}/{repo}/attachments"
            try:
                with open(file_path, 'rb') as fh:
                    ar = api_client.post(
                        attach_uri,
                        data={'access_token': access_token},
                        files={'file': (file_name, fh, 'application/octet-stream')},
                        timeout=UPLOAD_TIMEOUT,
                    )
                if ar.status_code in (200, 201):
                    aj = ar.json()
                    url = aj.get('url') or aj.get('browser_download_url') or aj.get('html_url')
                    if url:
                        line = f"- {file_name}: {url}"
                        if line not in body_additions:
                            body_additions.append(line)
                        print(f"Fallback: attached {file_name} via attachments, link collected to append.")
                        success = True
                    else:
                        print(f"Fallback: attachments API did not return a URL. Resp: {aj}")
                else:
                    print(f"Fallback: attachments upload failed {ar.status_code} {ar.text[:256]}")
            except requests.RequestException as ae:
                print(f"Fallback attachments error: {ae}")
            if not success:
                msg = f"Failed to upload {file_path} after {MAX_UPLOAD_RETRIES} attempts"
                if allow_partial:
                    print(msg + " (continue due to SYNC_CONTINUE_ON_UPLOAD_ERROR=true)")
                    continue
                else:
                    raise RuntimeError(msg)

    # 若收集到需要追加的链接，统一更新 release body 一次
    if body_additions:
        combined = (body or '').rstrip() + "\n\n" + "\n".join(body_additions)
        ur = api_client.patch(
            f"{release_api_uri}/{release_id}",
            data={
                'access_token': access_token,
                'body': combined,
                # Gitee 对 PATCH 可能要求带上这些字段
                'tag_name': tag,
                'name': tag,
            },
            timeout=REQUEST_TIMEOUT,
        )
        if ur.status_code in (200, 201):
            print("Release body updated with asset links.")
            body = combined
        else:
            print(f"Update release body failed: {ur.status_code} {ur.text}")

    # 仅保留最新 Release 以防超出 Gitee 仓库配额
    try:
        delete_gitee_releases(release_id, api_client,
                              release_api_uri, access_token)
    except ValueError as e:
        print(e)

    api_client.close()
    print("Sync is completed!")


def get_abs_path(path: str):
    wd = os.getcwd()
    return os.path.join(wd, path)


get_github_latest_release()
