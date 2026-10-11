"""Check that formula publication fails closed for incomplete/broken artifacts."""

import hashlib
import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "release_homebrew", Path(__file__).parents[1] / "scripts/release-homebrew.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FormulaTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.dist = Path(self.temp.name) / "release artifacts #{not_ruby}"
        self.dist.mkdir()
        checksums = []
        for system in ("darwin", "linux"):
            for arch in ("amd64", "arm64"):
                name = f"clivoy_1.2.3_{system}_{arch}.tar.gz"
                data = name.encode()
                (self.dist / name).write_bytes(data)
                checksums.append(f"{hashlib.sha256(data).hexdigest()}  {name}")
        (self.dist / "checksums.txt").write_text("\n".join(checksums))

    def test_each_platform_gets_its_verified_checksum(self):
        formula = MODULE.generate("v1.2.3", self.dist, "wklee610/clivoy")
        self.assertEqual(formula.count('      sha256 "'), 4)
        for line in (self.dist / "checksums.txt").read_text().splitlines():
            digest, filename = line.split()
            self.assertIn(
                f'      url "https://github.com/wklee610/clivoy/releases/download/v1.2.3/{filename}"\n'
                f'      sha256 "{digest}"', formula)

    def test_local_cli_uses_verified_local_uris(self):
        output = Path(self.temp.name) / "Formula" / "clivoy.rb"
        subprocess.run([
            sys.executable, str(Path(SPEC.origin)), "--tag", "v1.2.3",
            "--dist", str(self.dist), "--local", "--output", str(output),
        ], check=True, capture_output=True, text=True)
        formula = output.read_text()
        self.assertEqual(formula.count('      url "file://'), 4)
        self.assertIn("release%20artifacts%20%23%7Bnot_ruby%7D", formula)
        self.assertNotIn("#{not_ruby}", formula)
        self.assertNotIn("/releases/download/", formula)
        for line in (self.dist / "checksums.txt").read_text().splitlines():
            digest, filename = line.split()
            self.assertIn(
                f'      url "{(self.dist / filename).resolve().as_uri()}"\n'
                f'      sha256 "{digest}"', formula)

    def test_local_relative_path_is_resolved(self):
        # Resolve from the subprocess working directory, independent of where
        # the generated formula is stored.
        output = Path(self.temp.name) / "relative.rb"
        subprocess.run([
            sys.executable, str(Path(SPEC.origin)), "--tag", "v1.2.3",
            "--dist", self.dist.name, "--local", "--output", str(output),
        ], cwd=self.temp.name, check=True, capture_output=True, text=True)
        self.assertIn(f'url "{self.dist.resolve().as_uri()}/', output.read_text())

    @unittest.skipIf(sys.platform == "win32", "Windows filenames cannot contain quotes")
    def test_local_path_quotes_are_escaped_for_ruby(self):
        self.dist = self.dist.rename(self.dist.with_name('archives "#{not_ruby}"'))
        formula = MODULE.generate("v1.2.3", self.dist, "wklee610/clivoy", local=True)
        self.assertIn("archives%20%22%23%7Bnot_ruby%7D%22", formula)
        for line in formula.splitlines():
            if line.strip().startswith("url "):
                self.assertEqual(line.count('"'), 2)

    def test_modified_archive_is_rejected(self):
        (self.dist / "clivoy_1.2.3_linux_arm64.tar.gz").write_bytes(b"modified")
        for local in (False, True):
            with self.subTest(local=local), self.assertRaisesRegex(ValueError, "checksum mismatch"):
                MODULE.generate("v1.2.3", self.dist, "wklee610/clivoy", local=local)

    def test_missing_platform_is_rejected(self):
        (self.dist / "clivoy_1.2.3_darwin_amd64.tar.gz").unlink()
        for local in (False, True):
            with self.subTest(local=local), self.assertRaises(FileNotFoundError):
                MODULE.generate("v1.2.3", self.dist, "wklee610/clivoy", local=local)

    def test_unsafe_ruby_interpolation_is_rejected(self):
        for tag, repo in [('v1.2.3#{system("false")}', "wklee610/clivoy"),
                          ("v1.2.3", 'owner/repo"')]:
            for local in (False, True):
                with self.subTest(tag=tag, repo=repo, local=local), self.assertRaises(ValueError):
                    MODULE.generate(tag, self.dist, repo, local=local)


if __name__ == "__main__":
    unittest.main()
