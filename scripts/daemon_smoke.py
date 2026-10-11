#!/usr/bin/env python3
"""Test a built Clivoy daemon on Linux, macOS, and Windows with only stdlib.

Usage: python scripts/daemon_smoke.py ./build/clivoy
Each test owns a temporary CLIVOY_HOME. Only health and model discovery requests
are sent; no native AI CLI, cloud credential, or model request is used. Cleanup
uses Clivoy's authenticated stop command, never signals a PID from a state file.
"""

from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request


COMMAND_TIMEOUT = 25
HTTP_TIMEOUT = 2
EPHEMERAL_LISTEN = "127.0.0.1:0"


class DaemonSmoke(unittest.TestCase):
    binary: Path

    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="clivoy-daemon-smoke-")).resolve()
        self.home = self.root / "state"
        self.empty_bin = self.root / "empty-bin"
        self.empty_bin.mkdir()
        self.outputs = self.root / "commands"
        self.outputs.mkdir()
        self.env = {
            key: value for key, value in os.environ.items()
            if not key.upper().startswith("CLIVOY_")
        }
        self.env.update({
            "CLIVOY_HOME": str(self.home),
            # The absolute Clivoy binary still works. Model catalog discovery
            # only reads metadata; these tests never send inference requests.
            "PATH": str(self.empty_bin),
            "NO_PROXY": "127.0.0.1,localhost",
            "no_proxy": "127.0.0.1,localhost",
        })
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.output_lock = threading.Lock()
        self.output_number = 0
        self.secrets: set[str] = set()
        self.addresses: set[str] = set()
        self.addCleanup(self.cleanup)

    def command(self, *args, success=True):
        """Use files so an accidentally inherited stdout pipe cannot hang us."""
        with self.output_lock:
            self.output_number += 1
            output_path = self.outputs / f"{self.output_number:02d}.txt"
        with output_path.open("wb") as output:
            process = subprocess.Popen(
                [str(self.binary), *args], cwd=self.root, env=self.env,
                stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT,
            )
            try:
                code = process.wait(timeout=COMMAND_TIMEOUT)
            except subprocess.TimeoutExpired:
                # This is our own command process, never a PID read from JSON.
                process.kill()
                process.wait(timeout=5)
                self.fail(f"clivoy {args[0]} exceeded {COMMAND_TIMEOUT}s")
        text = output_path.read_text(encoding="utf-8", errors="replace")
        if success and code != 0:
            self.fail(f"clivoy {args[0]} exited {code}: {self.redacted(text)}")
        return code, text

    def redacted(self, text):
        self.collect_secrets()
        for secret in self.secrets:
            text = text.replace(secret, "[REDACTED]")
        return text[-3000:]

    def collect_secrets(self):
        key_path = self.home / "auth.json"
        if key_path.is_file():
            try:
                for key in json.loads(key_path.read_text(encoding="utf-8")).get("keys", []):
                    if isinstance(key, str) and key:
                        self.secrets.add(key)
            except (ValueError, OSError):
                pass
        state_path = self.home / "daemon.json"
        if state_path.is_file():
            try:
                token = json.loads(state_path.read_text(encoding="utf-8")).get("token")
                if isinstance(token, str) and token:
                    self.secrets.add(token)
            except (ValueError, OSError):
                pass

    def state(self):
        state_path = self.home / "daemon.json"
        self.assertTrue(state_path.is_file(), "start did not create daemon.json")
        data = json.loads(state_path.read_text(encoding="utf-8"))
        for field in ("api_address", "control_url", "pid", "config", "listen", "started_at", "token"):
            self.assertIn(field, data.keys(), f"daemon state lacks {field}")
        self.assertIsInstance(data["pid"], int)
        self.assertGreater(data["pid"], 0)
        for field in ("api_address", "control_url", "config", "listen", "started_at", "token"):
            self.assertIsInstance(data[field], str, f"invalid {field} type")
            self.assertTrue(data[field], f"empty {field}")
        self.secrets.add(data["token"])
        self.assertEqual(Path(data["config"]).resolve(), self.home / "clivoy.json")
        self.assertTrue(data["control_url"].startswith("http://127.0.0.1:"))
        address = self.api_url(data)
        parsed = urllib.parse.urlsplit(address)
        self.assertEqual(parsed.hostname, "127.0.0.1")
        self.assertGreater(parsed.port, 0)
        self.addresses.add(address)
        if os.name != "nt":
            self.assertEqual(state_path.stat().st_mode & 0o077, 0, "daemon.json must be owner-only")
        self.collect_secrets()
        return data

    @staticmethod
    def api_url(state):
        address = state["api_address"].rstrip("/")
        return address if address.startswith("http://") else "http://" + address

    def request(self, address, path, token=None):
        headers = {"Authorization": "Bearer " + token} if token else {}
        request = urllib.request.Request(address + path, headers=headers)
        try:
            with self.opener.open(request, timeout=HTTP_TIMEOUT) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            with error:
                return error.code, json.load(error)

    def check_ready(self, state):
        self.assertFalse((self.home / "tmp").exists(), "start created a persistent tmp directory")
        self.assertFalse(list(self.home.glob(".daemon-start-*")), "startup handshake was not removed")
        address = self.api_url(state)
        status, health = self.request(address, "/healthz")
        self.assertEqual(status, 200)
        self.assertEqual(health, {"status": "ok"})
        self.assertEqual(self.request(address, "/v1/models")[0], 401)
        self.assertEqual(self.request(address, "/v1/models", "invalid-smoke-token")[0], 401)
        key = json.loads((self.home / "auth.json").read_text(encoding="utf-8"))["keys"][0]
        self.assertTrue(key, "default init did not create an API key")
        self.assertIsNotNone(re.fullmatch(r"sk-clivoy-[0-9a-f]{64}", key), "invalid generated API key format")
        self.secrets.add(key)
        status, models = self.request(address, "/v1/models", key)
        self.assertEqual(status, 200)
        self.assertEqual(models["object"], "list")
        self.assertTrue({"codex", "claude", "gemini", "grok"}.issubset({model["id"] for model in models["data"]}))
        # The control credential must not authorize public API requests.
        self.assertEqual(self.request(address, "/v1/models", state["token"])[0], 401)

    def check_status(self, expected):
        _, output = self.command("status")
        self.assertIn(expected, output.lower())

    def check_closed(self, address):
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            try:
                self.request(address, "/healthz")
            except (urllib.error.URLError, OSError):
                return
            time.sleep(0.05)
        self.fail("daemon health endpoint still accepts requests after stop")

    def check_no_secret_output(self):
        self.collect_secrets()
        logs = self.home / "logs"
        paths = list(self.outputs.glob("*.txt"))
        if logs.is_dir():
            paths.extend(path for path in logs.rglob("*") if path.is_file())
        for path in paths:
            text = path.read_text(encoding="utf-8", errors="replace")
            for secret in self.secrets:
                if secret in text:
                    self.fail(f"API or control token leaked into {path.relative_to(self.root)}")

    def cleanup(self):
        self.collect_secrets()
        code, output = self.command("stop", success=False)
        if code != 0:
            # Preserve control state if stop failed so recovery remains possible.
            self.fail(f"cleanup stop failed; state retained at {self.home}: {self.redacted(output)}")
        try:
            for address in self.addresses:
                self.check_closed(address)
        except AssertionError:
            self.fail(f"cleanup left an active endpoint; state retained at {self.home}")
        try:
            self.check_no_secret_output()
            self.assertFalse((self.home / "tmp").exists(), "daemon command created a persistent tmp directory")
            self.assertFalse(list(self.home.glob(".daemon-start-*")), "startup handshake remained after stop")
        finally:
            shutil.rmtree(self.root)

    def test_start_duplicate_restart_stop(self):
        self.check_status("stopped")
        self.command("start", "--listen", EPHEMERAL_LISTEN)
        first = self.state()
        self.assertEqual(first["listen"], EPHEMERAL_LISTEN)
        self.check_ready(first)
        self.check_status("running")
        files = {name: (self.home / name).read_bytes() for name in ("clivoy.json", "auth.json")}

        self.command("start")
        duplicate = self.state()
        self.assertEqual(duplicate["pid"], first["pid"])
        self.assertEqual(duplicate["started_at"], first["started_at"])

        self.command("restart")
        restarted = self.state()
        self.assertNotEqual(restarted["pid"], first["pid"])
        self.assertEqual(restarted["config"], first["config"])
        self.assertEqual(restarted["listen"], first["listen"])
        self.check_ready(restarted)
        self.check_status("running")

        self.command("stop")
        self.check_status("stopped")
        self.check_closed(self.api_url(restarted))
        self.command("stop")
        self.check_status("stopped")
        for name, original in files.items():
            if (self.home / name).read_bytes() != original:
                self.fail(f"lifecycle commands changed {name}")
        self.assertTrue((self.home / "logs" / "clivoy.log").is_file())

    def test_auth_json_multiple_keys_and_revocation_on_restart(self):
        self.command("init")
        auth_path = self.home / "auth.json"
        first_key = json.loads(auth_path.read_text(encoding="utf-8"))["keys"][0]
        second_key = "sk-clivoy-" + "ab" * 32
        self.secrets.update((first_key, second_key))
        auth_path.write_text(json.dumps({"keys": [first_key, second_key]}), encoding="utf-8")
        self.command("start", "--listen", EPHEMERAL_LISTEN)
        initial = self.state()
        address = self.api_url(initial)
        for key in (first_key, second_key):
            self.assertEqual(self.request(address, "/v1/models", key)[0], 200)
        auth_path.write_text(json.dumps({"keys": [second_key]}), encoding="utf-8")
        self.assertEqual(self.request(address, "/v1/models", first_key)[0], 200)
        self.command("restart")
        address = self.api_url(self.state())
        self.assertEqual(self.request(address, "/v1/models", first_key)[0], 401)
        self.assertEqual(self.request(address, "/v1/models", second_key)[0], 200)

        override_key = "sk-clivoy-" + "cd" * 32
        self.secrets.add(override_key)
        self.env["CLIVOY_API_KEY"] = override_key
        self.command("restart")
        address = self.api_url(self.state())
        self.assertEqual(self.request(address, "/v1/models", second_key)[0], 401)
        self.assertEqual(self.request(address, "/v1/models", override_key)[0], 200)

    def test_occupied_port_start_fails_and_recovers(self):
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as occupied:
            if os.name == "nt":
                occupied.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
            occupied.bind(("127.0.0.1", 0))
            occupied.listen(1)
            listen = f"127.0.0.1:{occupied.getsockname()[1]}"
            code, _ = self.command("start", "--listen", listen, success=False)
            self.assertNotEqual(code, 0, "start succeeded with an occupied API port")
            self.check_status("stopped")
        self.command("start", "--listen", EPHEMERAL_LISTEN)
        self.check_ready(self.state())

    def test_concurrent_start_creates_one_daemon(self):
        barrier = threading.Barrier(2, timeout=5)

        def start():
            barrier.wait()
            return self.command("start", "--listen", EPHEMERAL_LISTEN, success=False)

        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(lambda _: start(), range(2)))
        self.assertTrue(any(code == 0 for code, _ in results), "both concurrent starts failed")
        first = self.state()
        self.check_ready(first)
        self.command("start")
        self.assertEqual(self.state()["pid"], first["pid"])
        self.command("stop")
        self.check_status("stopped")
        log = (self.home / "logs" / "clivoy.log").read_text(encoding="utf-8")
        self.assertEqual(log.count("server started"), 1, "concurrent start created multiple servers")

    def test_stale_state_is_stopped_and_start_recovers(self):
        self.command("start", "--listen", EPHEMERAL_LISTEN)
        first = self.state()
        self.check_ready(first)
        stale_bytes = (self.home / "daemon.json").read_bytes()
        self.command("stop")
        self.check_closed(self.api_url(first))
        # Reuse only our own stopped instance's state, never an arbitrary PID.
        state_path = self.home / "daemon.json"
        state_path.write_bytes(stale_bytes)
        state_path.chmod(0o600)
        self.check_status("stopped")
        self.command("start", "--listen", EPHEMERAL_LISTEN)
        recovered = self.state()
        self.assertNotEqual(recovered["pid"], first["pid"])
        self.check_ready(recovered)
        self.check_status("running")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("clivoy", type=Path, help="path to a built Clivoy executable")
    args = parser.parse_args()
    binary = args.clivoy.expanduser().resolve()
    if not binary.is_file():
        parser.error(f"Clivoy binary does not exist: {binary}")
    DaemonSmoke.binary = binary
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(DaemonSmoke)
    return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1


if __name__ == "__main__":
    raise SystemExit(main())
