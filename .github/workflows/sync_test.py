import importlib.util
import os
import sys
import tempfile
import unittest
from contextlib import ExitStack
from pathlib import Path
from unittest import mock

import requests


SCRIPT_PATH = Path(__file__).with_name("sync.py")
SPEC = importlib.util.spec_from_file_location("release_sync", SCRIPT_PATH)
release_sync = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = release_sync
SPEC.loader.exec_module(release_sync)


class FakeGitee:
    def __init__(self, existing=None):
        self.existing = existing or {}
        self.states = []
        self.session = mock.Mock()
        self.token = "token"

    def create_or_get_release(self, _release):
        return {"id": 7, "prerelease": False}

    def list_assets(self, _release_id):
        return dict(self.existing)

    def set_prerelease(self, _release_id, _release, prerelease):
        self.states.append(prerelease)

    def upload_url(self, _release_id):
        return "https://gitee.example/upload"


class SyncTests(unittest.TestCase):
    def test_parse_checksums(self):
        with tempfile.TemporaryDirectory() as directory:
            checksum_file = Path(directory) / "checksums.txt"
            checksum_file.write_text("a" * 64 + "  artifact.zip\n", encoding="utf-8")
            self.assertEqual(
                release_sync.parse_checksums(checksum_file),
                {"artifact.zip": "a" * 64},
            )

    def test_upload_timeout_is_confirmed_before_retry(self):
        client = FakeGitee(existing={"artifact.zip": {"name": "artifact.zip"}})
        client.session.post.side_effect = requests.Timeout("ambiguous timeout")
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / "artifact.zip"
            artifact.write_bytes(b"payload")
            with mock.patch.object(release_sync, "CONFIRM_DELAYS", (0,)):
                self.assertTrue(release_sync.upload_asset(client, 7, artifact))
        client.session.post.assert_called_once()

    def test_create_timeout_reuses_release_created_server_side(self):
        client = object.__new__(release_sync.GiteeClient)
        client.find_release = mock.Mock(side_effect=[None, {"id": 7, "tag_name": "v1.2.3"}])
        client.create_release = mock.Mock(side_effect=requests.Timeout("ambiguous timeout"))
        release = {"tag": "v1.2.3", "name": "v1.2.3", "body": ""}

        result = client.create_or_get_release(release)

        self.assertEqual(result["id"], 7)
        self.assertEqual(client.find_release.call_count, 2)

    def test_requested_release_tag_uses_exact_github_endpoint(self):
        response = mock.Mock(status_code=200)
        response.json.return_value = {
            "tag_name": "v1.2.3",
            "assets": [
                {
                    "name": "artifact.zip",
                    "browser_download_url": "https://example/artifact.zip",
                    "size": 7,
                }
            ],
        }
        session = mock.Mock()
        session.get.return_value = response

        with mock.patch.object(release_sync, "GITHUB_RELEASE_TAG", "v1.2.3"):
            release = release_sync.fetch_github_release(session, "token")

        self.assertEqual(release["tag"], "v1.2.3")
        requested_url = session.get.call_args.args[0]
        self.assertTrue(requested_url.endswith("/releases/tags/v1.2.3"))

    def test_release_is_staged_until_assets_are_complete(self):
        client = FakeGitee()
        release = {
            "tag": "v1.2.3",
            "name": "v1.2.3",
            "body": "",
            "assets": [release_sync.Asset("artifact.zip", "https://example/artifact.zip", 7)],
        }

        def fake_download(_session, _token, _asset, destination, _checksum=None):
            destination.write_bytes(b"payload")
            return destination

        with ExitStack() as stack:
            stack.enter_context(mock.patch.dict(os.environ, {"GITEE_TOKEN": "token"}, clear=False))
            stack.enter_context(mock.patch.object(release_sync, "build_session", return_value=mock.Mock()))
            stack.enter_context(mock.patch.object(release_sync, "GiteeClient", return_value=client))
            stack.enter_context(mock.patch.object(release_sync, "fetch_github_release", return_value=release))
            stack.enter_context(mock.patch.object(release_sync, "download_asset", side_effect=fake_download))
            stack.enter_context(mock.patch.object(release_sync, "upload_asset", return_value=True))
            stack.enter_context(mock.patch.object(release_sync, "wait_for_assets", return_value=set()))
            stack.enter_context(mock.patch.object(release_sync, "verify_latest_release"))
            stack.enter_context(mock.patch.object(release_sync, "prune_old_releases"))
            stack.enter_context(mock.patch.object(release_sync, "write_summary"))
            release_sync.sync_release()

        self.assertEqual(client.states, [True, False])

    def test_failed_upload_keeps_release_staged(self):
        client = FakeGitee()
        release = {
            "tag": "v1.2.3",
            "name": "v1.2.3",
            "body": "",
            "assets": [release_sync.Asset("artifact.zip", "https://example/artifact.zip", 7)],
        }

        def fake_download(_session, _token, _asset, destination, _checksum=None):
            destination.write_bytes(b"payload")
            return destination

        with ExitStack() as stack:
            stack.enter_context(mock.patch.dict(os.environ, {"GITEE_TOKEN": "token"}, clear=False))
            stack.enter_context(mock.patch.object(release_sync, "build_session", return_value=mock.Mock()))
            stack.enter_context(mock.patch.object(release_sync, "GiteeClient", return_value=client))
            stack.enter_context(mock.patch.object(release_sync, "fetch_github_release", return_value=release))
            stack.enter_context(mock.patch.object(release_sync, "download_asset", side_effect=fake_download))
            stack.enter_context(mock.patch.object(release_sync, "upload_asset", return_value=False))
            with self.assertRaises(release_sync.SyncError):
                release_sync.sync_release()

        self.assertEqual(client.states, [True])


if __name__ == "__main__":
    unittest.main()
