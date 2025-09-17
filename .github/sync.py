import os
import os
import time
import requests
from github import Github, Auth
import socket
import subprocess
import shlex

REQUEST_TIMEOUT = (10, 300)
UPLOAD_TIMEOUT = (10, 900)
MAX_DOWNLOAD_RETRIES = 3
MAX_UPLOAD_RETRIES = 5
SOCKET_TIMEOUT = 900


def get_github_latest_release():
    gh_token = os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')
    g = Github(auth=Auth.Token(gh_token)) if gh_token else Github()
    repo = g.get_repo('xOS/ServerStatus')
    release = repo.get_latest_release()
    if not release:
        print('No releases found.')
        return

    print(f"Latest release tag is: {release.tag_name}")
    print(f"Latest release info is: {release.body}")

    files = []
    asset_links = {}
    for asset in release.get_assets():
        url = asset.browser_download_url
        name = asset.name
        # 下载资产
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
        files.append(os.path.abspath(name))
        asset_links[name] = url

    sync_to_gitee(release.tag_name, release.body, files, asset_links)


def delete_gitee_releases(latest_id, client, uri, token):
    if not latest_id:
        print('Skip delete_gitee_releases: latest_id is empty')
        return
    params = {'access_token': token, 'page': 1, 'per_page': 100}
    resp = client.get(uri, params=params, timeout=REQUEST_TIMEOUT)
    if resp.status_code != 200:
        print(f"List releases failed: {resp.status_code} {resp.text}")
        return
    ids = [b.get('id') for b in resp.json() if 'id' in b]
    print(f"Current release ids: {ids}")
    if latest_id in ids:
        ids.remove(latest_id)
    for rid in ids:
        r = client.delete(f"{uri}/{rid}", params={'access_token': token}, timeout=REQUEST_TIMEOUT)
        if r.status_code == 204:
            print(f"Successfully deleted release #{rid}.")
        else:
            print(f"Delete release #{rid} failed: {r.status_code} {r.text}")


def sync_to_gitee(tag: str, body: str, files, asset_links=None):
    owner = os.environ.get('GITEE_OWNER', 'Ten')
    repo = os.environ.get('GITEE_REPO', 'ServerStatus')
    access_token = os.environ['GITEE_TOKEN']
    api = requests.Session()
    api.headers.update({'Accept': 'application/json'})

    release_api = f"https://gitee.com/api/v5/repos/{owner}/{repo}/releases"

    # 创建或复用 release
    form = {
        'access_token': access_token,
        'tag_name': tag,
        'name': tag,
        'body': body,
        'prerelease': 'false',
        'target_commitish': 'master',
    }
    r = api.post(release_api, data=form, timeout=REQUEST_TIMEOUT)
    if r.status_code == 201:
        rid = r.json().get('id')
    else:
        print(f"Create release failed: {r.status_code} {r.text}")
        lr = api.get(release_api, params={'access_token': access_token, 'page': 1, 'per_page': 100}, timeout=REQUEST_TIMEOUT)
        rid = None
        if lr.status_code == 200:
            for rel in lr.json():
                if rel.get('tag_name') == tag:
                    rid = rel.get('id')
                    break
        if not rid:
            raise RuntimeError(f"No Gitee release id available for tag {tag}")
    print(f"Gitee release id: {rid}")

    # 列出已有资产
    existing = {}
    rd = api.get(f"{release_api}/{rid}", params={'access_token': access_token}, timeout=REQUEST_TIMEOUT)
    if rd.status_code == 200:
        for a in (rd.json().get('assets') or []):
            if a.get('name'):
                existing[a['name']] = a.get('id')
    else:
        print(f"Get release detail failed: {rd.status_code} {rd.text}")

    # 按大小升序上传
    try:
        files = sorted(files, key=lambda p: os.path.getsize(p))
    except Exception:
        pass

    # 真实上传（attach_files）
    attach_api = f"{release_api}/{rid}/attach_files"

    for path in files:
        name = os.path.basename(path)
        try:
            size = os.path.getsize(path)
            print(f"Preparing to upload {name} ({size/1024/1024:.2f} MiB)")
        except OSError:
            print(f"Preparing to upload {name}")
        if name in existing:
            print(f"Skip upload {name}: already exists (asset id {existing[name]})")
            continue

        success = False
        for attempt in range(1, MAX_UPLOAD_RETRIES + 1):
            try:
                prev = socket.getdefaulttimeout()
                socket.setdefaulttimeout(SOCKET_TIMEOUT)
                try:
                    # 路径1：requests + data + files 字段
                    with open(path, 'rb') as fh:
                        resp = api.post(
                            attach_api,
                            data={'access_token': access_token},
                            files={'files': (name, fh, 'application/octet-stream')},
                            headers={'Expect': '100-continue', 'Connection': 'close'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if resp.status_code == 201:
                        print(f"Successfully uploaded {name}!")
                        success = True
                        break
                    else:
                        txt = resp.text
                        if resp.status_code in (400, 409, 422) and ('已存在' in txt or 'already exists' in txt):
                            print(f"Asset {name} already exists, treat as success.")
                            success = True
                            break
                        print(f"Upload failed (status {resp.status_code}) for {name}, attempt {attempt}/{MAX_UPLOAD_RETRIES}. Body: {txt[:256]}")

                    # 路径2：requests + params + files 字段
                    with open(path, 'rb') as fh:
                        resp2 = api.post(
                            attach_api,
                            params={'access_token': access_token},
                            files={'files': (name, fh, 'application/octet-stream')},
                            headers={'Connection': 'close'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if resp2.status_code == 201:
                        print(f"Successfully uploaded via alt path: {name}!")
                        success = True
                        break
                    else:
                        txt2 = resp2.text
                        if resp2.status_code in (400, 409, 422) and ('已存在' in txt2 or 'already exists' in txt2):
                            print(f"Asset {name} already exists (alt), treat as success.")
                            success = True
                            break
                        print(f"Alt upload failed (status {resp2.status_code}) for {name}. Body: {txt2[:256]}")

                    # 路径2b：requests + data + file 字段
                    with open(path, 'rb') as fh:
                        resp2b = api.post(
                            attach_api,
                            data={'access_token': access_token},
                            files={'file': (name, fh, 'application/octet-stream')},
                            headers={'Connection': 'close'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if resp2b.status_code == 201:
                        print(f"Successfully uploaded via alt path (file field): {name}!")
                        success = True
                        break
                    else:
                        txt2b = resp2b.text
                        if resp2b.status_code in (400, 409, 422) and ('已存在' in txt2b or 'already exists' in txt2b):
                            print(f"Asset {name} already exists (file field), treat as success.")
                            success = True
                            break
                        print(f"Alt upload (file field) failed (status {resp2b.status_code}) for {name}. Body: {txt2b[:256]}")

                    # 路径2c：requests + params + file 字段
                    with open(path, 'rb') as fh:
                        resp2c = api.post(
                            attach_api,
                            params={'access_token': access_token},
                            files={'file': (name, fh, 'application/octet-stream')},
                            headers={'Connection': 'close'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if resp2c.status_code == 201:
                        print(f"Successfully uploaded via alt path (params+file): {name}!")
                        success = True
                        break
                    else:
                        txt2c = resp2c.text
                        if resp2c.status_code in (400, 409, 422) and ('已存在' in txt2c or 'already exists' in txt2c):
                            print(f"Asset {name} already exists (params+file), treat as success.")
                            success = True
                            break
                        print(f"Alt upload (params+file) failed (status {resp2c.status_code}) for {name}. Body: {txt2c[:256]}")

                    # 路径3：curl files 字段
                    curl_cmd = (
                        f"curl -sS -f --http1.1 -4 -X POST "
                        f"--connect-timeout 10 --max-time {SOCKET_TIMEOUT} "
                        f"-F files=@{shlex.quote(path)};type=application/octet-stream "
                        f"-F access_token={shlex.quote(access_token)} "
                        f"{shlex.quote(attach_api)}"
                    )
                    rc = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                    if rc.returncode == 0:
                        print(f"Successfully uploaded via curl: {name}!")
                        success = True
                        break
                    else:
                        print(f"curl upload failed (exit {rc.returncode}) for {name}. stderr: {rc.stderr[:256]}")

                    # 路径3b：curl file 字段
                    curl_cmd2 = (
                        f"curl -sS -f --http1.1 -4 -X POST "
                        f"--connect-timeout 10 --max-time {SOCKET_TIMEOUT} "
                        f"-F file=@{shlex.quote(path)};type=application/octet-stream "
                        f"-F access_token={shlex.quote(access_token)} "
                        f"{shlex.quote(attach_api)}"
                    )
                    rc2 = subprocess.run(curl_cmd2, shell=True, capture_output=True, text=True)
                    if rc2.returncode == 0:
                        print(f"Successfully uploaded via curl(file field): {name}!")
                        success = True
                        break
                    else:
                        print(f"curl(file field) upload failed (exit {rc2.returncode}) for {name}. stderr: {rc2.stderr[:256]}")

                finally:
                    socket.setdefaulttimeout(prev)

            except requests.RequestException as e:
                print(f"Upload error for {name}: {e} (attempt {attempt}/{MAX_UPLOAD_RETRIES})")
                # 异常路径直接尝试备用 requests 与 curl
                try:
                    with open(path, 'rb') as fh:
                        r2 = api.post(
                            attach_api,
                            params={'access_token': access_token},
                            files={'files': (name, fh, 'application/octet-stream')},
                            headers={'Connection': 'close'},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if r2.status_code == 201:
                        print(f"Successfully uploaded via alt path (exception): {name}!")
                        success = True
                        break
                    else:
                        print(f"Alt path (exception) failed (status {r2.status_code}) for {name}. Body: {r2.text[:256]}")
                except requests.RequestException as e3:
                    print(f"Alt path request error (exception) for {name}: {e3}")
                curl_cmd = (
                    f"curl -sS -f --http1.1 -4 -X POST "
                    f"--connect-timeout 10 --max-time {SOCKET_TIMEOUT} "
                    f"-F files=@{shlex.quote(path)};type=application/octet-stream "
                    f"-F access_token={shlex.quote(access_token)} "
                    f"{shlex.quote(attach_api)}"
                )
                rc = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                if rc.returncode == 0:
                    print(f"Successfully uploaded via curl (exception outer): {name}!")
                    success = True
                    break
                else:
                    print(f"curl (exception outer) failed (exit {rc.returncode}) for {name}. stderr: {rc.stderr[:256]}")

            time.sleep(min(60, 2 ** (attempt - 1)))

        if not success:
            # 兜底：attachments 接口
            attach_uri = f"https://gitee.com/api/v5/repos/{owner}/{repo}/attachments"
            try:
                with open(path, 'rb') as fh:
                    ar = api.post(
                        attach_uri,
                        data={'access_token': access_token},
                        files={'file': (name, fh, 'application/octet-stream')},
                        headers={'Connection': 'close'},
                        timeout=UPLOAD_TIMEOUT,
                    )
                if ar.status_code in (200, 201):
                    print(f"Fallback: uploaded attachment for {name}.")
                    success = True
                else:
                    print(f"Fallback: attachments upload failed {ar.status_code} {ar.text[:256]}")
                if not success:
                    curl_cmd = (
                        f"curl -sS -f --http1.1 -4 -X POST "
                        f"--connect-timeout 10 --max-time {SOCKET_TIMEOUT} "
                        f"-F file=@{shlex.quote(path)};type=application/octet-stream "
                        f"-F access_token={shlex.quote(access_token)} "
                        f"https://gitee.com/api/v5/repos/{owner}/{repo}/attachments"
                    )
                    rc = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                    if rc.returncode == 0:
                        print(f"Fallback via curl: uploaded attachment for {name}.")
                        success = True
                    else:
                        print(f"curl attachments (exception) failed (exit {rc.returncode}) for {name}. stderr: {rc.stderr[:256]}")
            except requests.RequestException as ae:
                print(f"Fallback attachments error: {ae}")
            if not success:
                raise RuntimeError(f"Failed to upload {path} after {MAX_UPLOAD_RETRIES} attempts")

    # 清理旧 release（保留最新）
    try:
        delete_gitee_releases(rid, api, release_api, access_token)
    except Exception as e:
        print(e)

    api.close()
    print('Sync is completed!')


if __name__ == '__main__':
    get_github_latest_release()
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
    # 链接模式：跳过上传（用户要求真实上传，此处默认关闭）
    link_only = os.environ.get('SYNC_LINK_ONLY', 'false').lower() == 'true'
    try:
        # 默认不启用按大小走链接模式（需显式设置该环境变量）
        link_threshold_mb = float(os.environ.get('SYNC_LINK_THRESHOLD_MB', '0'))
    except ValueError:
        link_threshold_mb = 0.0

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
                            # Gitee attach_files 接口参数名为 files（可多文件）
                            files={'files': (file_name, fh, 'application/octet-stream')},
                            headers={'Expect': '100-continue', 'Connection': 'close'},
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
                                files={'files': (file_name, fh, 'application/octet-stream')},
                                headers={'Connection': 'close'},
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
                        # 第三通道：使用 curl（对某些服务器/网络更稳定）
                        curl_cmd = (
                            f"curl -sS -f -X POST "
                            f"--connect-timeout 10 --max-time {UPLOAD_SOCKET_TIMEOUT} "
                            f"-F files=@{shlex.quote(file_path)};type=application/octet-stream "
                            f"-F access_token={shlex.quote(access_token)} "
                            f"{shlex.quote(asset_api_uri)}"
                        )
                        try:
                            r = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                            if r.returncode == 0:
                                print(f"Successfully uploaded via curl: {file_name}!")
                                success = True
                                break
                            else:
                                print(f"curl upload failed (exit {r.returncode}) for {file_name}. stderr: {r.stderr[:256]}")
                        except Exception as ce:
                            print(f"curl upload error for {file_name}: {ce}")
                    except requests.RequestException as e2:
                        print(f"Alt upload error for {file_path}: {e2}")
                        # 在请求异常时也尝试 curl
                        curl_cmd = (
                            f"curl -sS -f -X POST "
                            f"--connect-timeout 10 --max-time {UPLOAD_SOCKET_TIMEOUT} "
                            f"-F files=@{shlex.quote(file_path)};type=application/octet-stream "
                            f"-F access_token={shlex.quote(access_token)} "
                            f"{shlex.quote(asset_api_uri)}"
                        )
                        try:
                            r = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                            if r.returncode == 0:
                                print(f"Successfully uploaded via curl (exception path): {file_name}!")
                                success = True
                                break
                            else:
                                print(f"curl upload failed (exit {r.returncode}) for {file_name}. stderr: {r.stderr[:256]}")
                        except Exception as ce:
                            print(f"curl upload error for {file_name}: {ce}")
            except requests.RequestException as e:
                print(f"Upload error for {file_path}: {e} (attempt {attempt}/{MAX_UPLOAD_RETRIES})")
                # 异常路径：立刻尝试备用 requests 通道与 curl
                try:
                    with open(file_path, 'rb') as fh:
                        resp2 = api_client.post(
                            asset_api_uri,
                            params={'access_token': access_token},
                            files={'files': (file_name, fh, 'application/octet-stream')},
                            timeout=UPLOAD_TIMEOUT,
                        )
                    if resp2.status_code == 201:
                        asset_info = resp2.json()
                        asset_name = asset_info.get('name')
                        print(f"Successfully uploaded via alt path (exception): {asset_name}!")
                        success = True
                        break
                    else:
                        print(f"Alt path (exception) failed (status {resp2.status_code}) for {file_path}. Body: {resp2.text[:256]}")
                except requests.RequestException as e3:
                    print(f"Alt path request error (exception) for {file_path}: {e3}")
                # 尝试 curl
                curl_cmd = (
                    f"curl -sS -f -X POST "
                    f"--connect-timeout 10 --max-time {UPLOAD_SOCKET_TIMEOUT} "
                    f"-F files=@{shlex.quote(file_path)};type=application/octet-stream "
                    f"-F access_token={shlex.quote(access_token)} "
                    f"{shlex.quote(asset_api_uri)}"
                )
                try:
                    r = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                    if r.returncode == 0:
                        print(f"Successfully uploaded via curl (exception outer): {file_name}!")
                        success = True
                        break
                    else:
                        print(f"curl (exception outer) failed (exit {r.returncode}) for {file_name}. stderr: {r.stderr[:256]}")
                except Exception as ce:
                    print(f"curl (exception outer) error for {file_name}: {ce}")
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
                        headers={'Connection': 'close'},
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
                if not success:
                    # 尝试使用 curl 走 attachments 接口
                    curl_cmd = (
                        f"curl -sS -f -X POST "
                        f"--connect-timeout 10 --max-time {UPLOAD_SOCKET_TIMEOUT} "
                        f"-F file=@{shlex.quote(file_path)};type=application/octet-stream "
                        f"-F access_token={shlex.quote(access_token)} "
                        f"https://gitee.com/api/v5/repos/{owner}/{repo}/attachments"
                    )
                    try:
                        r = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                        if r.returncode == 0:
                            print(f"Fallback via curl: uploaded attachment for {file_name}. Will add link if API returns it on body update.")
                            # 我们无法直接得到URL，留给后续人工查看附件列表或忽略
                            success = True
                        else:
                            print(f"curl attachments failed (exit {r.returncode}) for {file_name}. stderr: {r.stderr[:256]}")
                    except Exception as ce:
                        print(f"curl attachments error for {file_name}: {ce}")
            except requests.RequestException as ae:
                print(f"Fallback attachments error: {ae}")
                # 尝试使用 curl 走 attachments 接口
                curl_cmd = (
                    f"curl -sS -f -X POST "
                    f"--connect-timeout 10 --max-time {UPLOAD_SOCKET_TIMEOUT} "
                    f"-F file=@{shlex.quote(file_path)};type=application/octet-stream "
                    f"-F access_token={shlex.quote(access_token)} "
                    f"https://gitee.com/api/v5/repos/{owner}/{repo}/attachments"
                )
                try:
                    r = subprocess.run(curl_cmd, shell=True, capture_output=True, text=True)
                    if r.returncode == 0:
                        print(f"Fallback via curl: uploaded attachment for {file_name}.")
                        success = True
                    else:
                        print(f"curl attachments (exception) failed (exit {r.returncode}) for {file_name}. stderr: {r.stderr[:256]}")
                except Exception as ce:
                    print(f"curl attachments (exception) error for {file_name}: {ce}")
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
