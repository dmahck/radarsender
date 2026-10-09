"""Offline, non-mutating verification of built universal and small installers."""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("radarsender_build", ROOT / "tools" / "build.py")
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)
VERSION = build.VERSION
OUT = ROOT / "dist" / VERSION


class BuiltPackages(unittest.TestCase):
    @unittest.skipIf(os.name == "nt", "Unix executable permissions are checked on Linux")
    def test_direct_binaries_are_executable(self):
        for target in build.TARGETS:
            for name in ("radarsender", "tcpdump"):
                self.assertTrue(os.access(OUT / target / name, os.X_OK), (target, name))

    def test_outer_hashes(self):
        for line in (OUT / "SHA256SUMS").read_text().splitlines():
            expected, name = line.split("  ", 1)
            self.assertEqual(hashlib.sha256((OUT / name).read_bytes()).hexdigest(), expected, name)

    def test_wrappers_payloads_and_licenses(self):
        for target in ("universal", *build.TARGETS):
            with self.subTest(target=target):
                folder = OUT if target == "universal" else OUT / target
                installer = folder / f"luci-app-radarsender_{VERSION}_{target}.run"
                wrapper, payload = installer.read_bytes().split(b"__RADARSENDER_PAYLOAD__\n", 1)
                self.assertIn(hashlib.sha256(payload).hexdigest().encode(), wrapper)
                archive = folder / (f"luci-app-radarsender_{VERSION}_universal.tar.gz" if target == "universal" else f"radarsender-{VERSION}-{target}.tar.gz")
                self.assertEqual(payload, archive.read_bytes())
                with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as tar:
                    members = tar.getmembers()
                    for member in members:
                        self.assertTrue(member.isfile())
                        self.assertFalse(member.name.startswith("/"))
                        self.assertNotIn("..", member.name.split("/"))
                    files = {member.name: tar.extractfile(member).read() for member in members}
                    for line in files["SHA256SUMS"].decode().splitlines():
                        expected, name = line.split("  ", 1)
                        self.assertEqual(hashlib.sha256(files[name]).hexdigest(), expected, name)
                    for name in ("radarsender-LICENSE", "upstream-NOTICES-LICENSE", "THIRD_PARTY_NOTICES.md", "tcpdump-4.99.7-LICENSE", "libpcap-1.11.0-LICENSE", "musl-arm-LICENSE", "gcc-runtime-LICENSE", "gcc-mipsel-LICENSE"):
                        self.assertTrue(files["common/usr/lib/radarsender/licenses/" + name])
                    targets = {m.name.split("/")[1] for m in members if m.name.startswith("targets/")}
                    self.assertEqual(targets, set(build.TARGETS) if target == "universal" else {target})
                    for member in members:
                        if member.name.startswith("targets/"):
                            self.assertEqual(member.mode, 0o755)
                if target != "universal":
                    self.assertLess(installer.stat().st_size, (OUT / f"luci-app-radarsender_{VERSION}_universal.run").stat().st_size / 2)


if __name__ == "__main__":
    unittest.main()
