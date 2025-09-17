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
