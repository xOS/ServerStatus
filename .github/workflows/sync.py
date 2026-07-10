#!/usr/bin/env python3

import hashlib
import os
import sys
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import quote

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


GITHUB_REPO = os.getenv("GITHUB_REPO", "xOS/ServerAgent")
GITHUB_RELEASE_TAG = os.getenv("GITHUB_RELEASE_TAG", "").strip()
GITEE_OWNER = os.getenv("GITEE_OWNER", "Ten")
GITEE_REPO = os.getenv("GITEE_REPO", "ServerAgent")
GITEE_TARGET = os.getenv("GITEE_TARGET", "master")

API_TIMEOUT = (15, 60)
DOWNLOAD_TIMEOUT = (15, 300)
UPLOAD_TIMEOUT = (15, int(os.getenv("GITEE_UPLOAD_READ_TIMEOUT", "120")))
UPLOAD_ATTEMPTS = int(os.getenv("GITEE_UPLOAD_ATTEMPTS", "1"))
CONFIRM_DELAYS = (0, 5, 10, 15, 20)
RETRYABLE_STATUS_CODES = (408, 425, 429, 500, 502, 503, 504)


class SyncError(RuntimeError):
    pass


@dataclass(frozen=True)
class Asset:
    name: str
    url: str
    size: int


def build_session():
    session = requests.Session()
    retry = Retry(
        total=4,
        connect=4,
        read=4,
        status=4,
        backoff_factor=1.5,
        status_forcelist=RETRYABLE_STATUS_CODES,
        allowed_methods=frozenset(["GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"]),
        raise_on_status=False,
    )
    adapter = HTTPAdapter(max_retries=retry, pool_connections=4, pool_maxsize=4)
    session.mount("https://", adapter)
    session.headers.update({
        "Accept": "application/json",
        "User-Agent": "ServerStatus-Gitee-Release-Sync",
    })
    return session


def response_message(response):
    try:
        payload = response.json()
        if isinstance(payload, dict):
            return str(payload.get("message") or payload)
        return str(payload)
    except ValueError:
        return response.text.strip()


def require_status(response, expected, operation):
    if response.status_code not in expected:
        message = response_message(response)[:500]
        raise SyncError(f"{operation} failed with HTTP {response.status_code}: {message}")


def validate_asset_name(name):
    if not name or name in (".", "..") or "/" in name or "\\" in name:
        raise SyncError(f"unsafe release asset name: {name!r}")


def fetch_github_release(session, token):
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    release_path = (
        f"releases/tags/{quote(GITHUB_RELEASE_TAG, safe='')}"
        if GITHUB_RELEASE_TAG
        else "releases/latest"
    )
    response = session.get(
        f"https://api.github.com/repos/{GITHUB_REPO}/{release_path}",
        headers=headers,
        timeout=API_TIMEOUT,
    )
    require_status(response, (200,), "fetch latest GitHub release")
    payload = response.json()
    tag = payload.get("tag_name")
    if not tag:
        raise SyncError("latest GitHub release has no tag_name")
    if GITHUB_RELEASE_TAG and tag != GITHUB_RELEASE_TAG:
        raise SyncError(
            f"GitHub release tag mismatch: requested {GITHUB_RELEASE_TAG}, received {tag}"
        )

    assets = []
    seen = set()
    for item in payload.get("assets") or []:
        name = item.get("name") or ""
        validate_asset_name(name)
        if name in seen:
            raise SyncError(f"duplicate GitHub release asset: {name}")
        seen.add(name)
        url = item.get("browser_download_url") or ""
        if not url.startswith("https://"):
            raise SyncError(f"invalid download URL for {name}")
        assets.append(Asset(name=name, url=url, size=int(item.get("size") or 0)))
    if not assets:
        raise SyncError("latest GitHub release has no assets")

    return {
        "tag": tag,
        "name": payload.get("name") or tag,
        "body": payload.get("body") or "",
        "assets": assets,
    }


class GiteeClient:
    def __init__(self, session, token):
        if not token:
            raise SyncError("GITEE_TOKEN is not set")
        self.session = session
        self.token = token
        self.base = f"https://gitee.com/api/v5/repos/{GITEE_OWNER}/{GITEE_REPO}"

    def request(self, method, path, expected=(200,), **kwargs):
        if method in ("GET", "DELETE"):
            params = dict(kwargs.pop("params", {}))
            params["access_token"] = self.token
            kwargs["params"] = params
        else:
            data = dict(kwargs.pop("data", {}))
            data["access_token"] = self.token
            kwargs["data"] = data
        response = self.session.request(
            method,
            self.base + path,
            timeout=kwargs.pop("timeout", API_TIMEOUT),
            **kwargs,
        )
        require_status(response, expected, f"{method} Gitee {path}")
        if response.status_code == 204 or not response.content:
            return None
        return response.json()

    def list_releases(self):
        releases = []
        for page in range(1, 101):
            batch = self.request("GET", "/releases", params={"page": page, "per_page": 100})
            if not isinstance(batch, list):
                raise SyncError("Gitee releases response is not a list")
            releases.extend(batch)
            if len(batch) < 100:
                break
        return releases

    def find_release(self, tag):
        for release in self.list_releases():
            if release.get("tag_name") == tag:
                return release
        return None

    def create_release(self, release):
        payload = self.request(
            "POST",
            "/releases",
            expected=(201,),
            data={
                "tag_name": release["tag"],
                "name": release["name"],
                "body": release["body"],
                "target_commitish": GITEE_TARGET,
                "prerelease": "true",
            },
        )
        print(f"Created staged Gitee release {release['tag']} (id={payload.get('id')}).")
        return payload

    def create_or_get_release(self, release):
        existing = self.find_release(release["tag"])
        if existing:
            print(f"Reuse Gitee release {release['tag']} (id={existing.get('id')}).")
            return existing
        try:
            return self.create_release(release)
        except (requests.RequestException, SyncError):
            existing = self.find_release(release["tag"])
            if existing:
                print(f"Found Gitee release created concurrently (id={existing.get('id')}).")
                return existing
            raise

    def set_prerelease(self, release_id, release, prerelease):
        payload = self.request(
            "PATCH",
            f"/releases/{release_id}",
            data={
                "tag_name": release["tag"],
                "name": release["name"],
                "body": release["body"],
                "prerelease": "true" if prerelease else "false",
            },
        )
        state = "staged" if prerelease else "published"
        print(f"Gitee release {release['tag']} is now {state}.")
        return payload

    def list_assets(self, release_id):
        assets = {}
        for page in range(1, 101):
            batch = self.request(
                "GET",
                f"/releases/{release_id}/attach_files",
                params={"page": page, "per_page": 100},
            )
            if not isinstance(batch, list):
                raise SyncError("Gitee release assets response is not a list")
            for item in batch:
                name = item.get("name")
                if name:
                    assets[name] = item
            if len(batch) < 100:
                break
        return assets

    def upload_url(self, release_id):
        return self.base + f"/releases/{release_id}/attach_files"

    def delete_release(self, release_id):
        self.request("DELETE", f"/releases/{release_id}", expected=(204,))

    def latest_release(self):
        return self.request("GET", "/releases/latest")


def retry_delay(attempt):
    return min(30, 2 ** attempt)


def download_asset(session, token, asset, destination, expected_checksum=None):
    headers = {"Accept": "application/octet-stream"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    partial = destination.with_suffix(destination.suffix + ".part")

    for attempt in range(1, 4):
        digest = hashlib.sha256()
        written = 0
        try:
            with session.get(
                asset.url,
                headers=headers,
                timeout=DOWNLOAD_TIMEOUT,
                stream=True,
            ) as response:
                require_status(response, (200,), f"download {asset.name}")
                with partial.open("wb") as output:
                    for chunk in response.iter_content(chunk_size=1024 * 1024):
                        if not chunk:
                            continue
                        output.write(chunk)
                        digest.update(chunk)
                        written += len(chunk)
            if asset.size and written != asset.size:
                raise SyncError(
                    f"{asset.name} size mismatch: expected {asset.size}, downloaded {written}"
                )
            actual_checksum = digest.hexdigest()
            if expected_checksum and actual_checksum.lower() != expected_checksum.lower():
                raise SyncError(
                    f"{asset.name} checksum mismatch: expected {expected_checksum}, "
                    f"downloaded {actual_checksum}"
                )
            os.replace(partial, destination)
            print(f"Downloaded and verified {asset.name} ({written} bytes).")
            return destination
        except (requests.RequestException, SyncError) as error:
            partial.unlink(missing_ok=True)
            if attempt >= 3:
                raise SyncError(f"download {asset.name} failed: {error}") from error
            delay = retry_delay(attempt)
            print(f"Download {asset.name} attempt {attempt} failed: {error}; retry in {delay}s.")
            time.sleep(delay)

    raise SyncError(f"download {asset.name} failed unexpectedly")


def parse_checksums(path):
    checksums = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        fields = raw_line.split()
        if len(fields) < 2:
            continue
        checksum = fields[0].lower()
        name = fields[1].lstrip("*")
        if len(checksum) != 64 or any(char not in "0123456789abcdef" for char in checksum):
            raise SyncError(f"invalid SHA-256 entry for {name}")
        checksums[name] = checksum
    if not checksums:
        raise SyncError("checksums.txt contains no valid entries")
    return checksums


def confirm_asset(client, release_id, asset_name):
    for delay in CONFIRM_DELAYS:
        if delay:
            time.sleep(delay)
        try:
            if asset_name in client.list_assets(release_id):
                print(f"Confirmed {asset_name} on Gitee after an ambiguous upload result.")
                return True
        except (requests.RequestException, SyncError) as error:
            print(f"Could not confirm {asset_name}: {error}")
    return False


def upload_asset(client, release_id, path):
    asset_name = path.name
    for attempt in range(1, UPLOAD_ATTEMPTS + 1):
        response = None
        error = None
        try:
            with path.open("rb") as file_handle:
                response = client.session.post(
                    client.upload_url(release_id),
                    params={"access_token": client.token},
                    files={"file": (asset_name, file_handle, "application/octet-stream")},
                    timeout=UPLOAD_TIMEOUT,
                )
            if response.status_code == 201:
                print(f"Uploaded {asset_name}.")
                return True
            message = response_message(response)
            print(
                f"Upload {asset_name} returned HTTP {response.status_code}: "
                f"{message[:300]}"
            )
            lower_message = message.lower()
            if response.status_code in (400, 409, 422) and (
                "already" in lower_message or "exists" in lower_message or "已存在" in message
            ):
                return True
            if response.status_code in (401, 403, 413):
                raise SyncError(
                    f"upload {asset_name} cannot be retried: HTTP {response.status_code}: {message}"
                )
        except (requests.Timeout, requests.ConnectionError) as exc:
            error = exc
            print(f"Upload {asset_name} had an ambiguous network result: {exc}")

        if confirm_asset(client, release_id, asset_name):
            return True
        if attempt < UPLOAD_ATTEMPTS:
            delay = retry_delay(attempt)
            print(f"Retrying upload of {asset_name} in {delay}s.")
            time.sleep(delay)
        elif error:
            print(f"Upload {asset_name} was not confirmed after timeout: {error}")
    return False


def wait_for_assets(client, release_id, expected_names):
    missing = set(expected_names)
    for attempt in range(1, 9):
        remote_names = set(client.list_assets(release_id))
        missing = set(expected_names) - remote_names
        if not missing:
            return set()
        if attempt < 8:
            print(f"Waiting for Gitee asset index; still missing: {sorted(missing)}")
            time.sleep(10)
    return missing


def prune_old_releases(client, current_id):
    for release in client.list_releases():
        release_id = release.get("id")
        if not release_id or release_id == current_id:
            continue
        try:
            client.delete_release(release_id)
            print(f"Deleted old Gitee release {release.get('tag_name')} (id={release_id}).")
        except (requests.RequestException, SyncError) as error:
            print(f"Warning: failed to delete old Gitee release {release_id}: {error}")


def verify_latest_release(client, expected_tag):
    latest = {}
    for delay in CONFIRM_DELAYS:
        if delay:
            time.sleep(delay)
        latest = client.latest_release()
        if latest.get("tag_name") == expected_tag and not latest.get("prerelease"):
            return
    raise SyncError(
        f"Gitee latest release verification failed: expected {expected_tag}, "
        f"got {latest.get('tag_name')}"
    )


def write_summary(release, uploaded, existing):
    summary_path = os.getenv("GITHUB_STEP_SUMMARY")
    if not summary_path:
        return
    with open(summary_path, "a", encoding="utf-8") as summary:
        summary.write("## Gitee release synchronization\n\n")
        summary.write(f"- Release: `{release['tag']}`\n")
        summary.write(f"- Already present: {existing}\n")
        summary.write(f"- Uploaded or confirmed: {uploaded}\n")
        summary.write(f"- Expected assets: {len(release['assets'])}\n")


def sync_release():
    github_token = os.getenv("GITHUB_TOKEN") or os.getenv("GH_TOKEN")
    gitee_token = os.getenv("GITEE_TOKEN")
    session = build_session()
    gitee = GiteeClient(session, gitee_token)

    release = fetch_github_release(session, github_token)
    print(f"GitHub latest release: {release['tag']} ({len(release['assets'])} assets).")
    gitee_release = gitee.create_or_get_release(release)
    release_id = gitee_release.get("id")
    if not release_id:
        raise SyncError("Gitee release has no id")

    expected = {asset.name: asset for asset in release["assets"]}
    existing_assets = gitee.list_assets(release_id)
    missing_names = set(expected) - set(existing_assets)
    existing_count = len(expected) - len(missing_names)

    if missing_names:
        if not gitee_release.get("prerelease"):
            gitee.set_prerelease(release_id, release, True)
        print(f"Missing Gitee assets: {sorted(missing_names)}")

        checksum_asset = expected.get("checksums.txt")
        with tempfile.TemporaryDirectory(prefix="gitee-release-sync-") as temp_dir:
            temp_path = Path(temp_dir)
            checksums = {}
            checksum_path = None
            if checksum_asset:
                checksum_path = download_asset(
                    session,
                    github_token,
                    checksum_asset,
                    temp_path / checksum_asset.name,
                )
                checksums = parse_checksums(checksum_path)

            failed = []
            ordered_missing = sorted(
                (expected[name] for name in missing_names),
                key=lambda asset: (asset.name != "checksums.txt", asset.size, asset.name),
            )
            for asset in ordered_missing:
                if asset.name == "checksums.txt" and checksum_path:
                    local_path = checksum_path
                else:
                    expected_checksum = checksums.get(asset.name)
                    if checksums and not expected_checksum:
                        raise SyncError(f"checksums.txt has no entry for {asset.name}")
                    local_path = download_asset(
                        session,
                        github_token,
                        asset,
                        temp_path / asset.name,
                        expected_checksum,
                    )
                if not upload_asset(gitee, release_id, local_path):
                    failed.append(asset.name)

            if failed:
                raise SyncError(
                    "Gitee uploads remain unconfirmed; rerun the workflow to resume: "
                    + ", ".join(failed)
                )

    remaining = wait_for_assets(gitee, release_id, expected)
    if remaining:
        raise SyncError(
            "Gitee release remains staged because assets are missing: "
            + ", ".join(sorted(remaining))
        )

    gitee.set_prerelease(release_id, release, False)
    verify_latest_release(gitee, release["tag"])

    prune_old_releases(gitee, release_id)
    uploaded_count = len(missing_names)
    write_summary(release, uploaded_count, existing_count)
    print(
        f"Synchronization completed: {release['tag']}, "
        f"{len(expected)} assets available on Gitee."
    )


if __name__ == "__main__":
    try:
        sync_release()
    except (requests.RequestException, SyncError, OSError, ValueError) as error:
        print(f"Sync failed: {error}", file=sys.stderr)
        raise SystemExit(1)
