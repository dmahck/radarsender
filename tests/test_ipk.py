"""Offline IPK structure and release-provenance checks; never installs a package.

These checks do not establish compatibility with a real opkg installation or
native MT7621 capture. Build the IPKs first with tools/build_ipk.py.
"""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import struct
import tarfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("radarsender_ipk_build", ROOT / "tools" / "build_ipk.py")
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)
OUT = Path(build.OUT)
VERSION = "0.1.6-1"
RELEASE = "0.1.6"
BUNDLE_BASE = Path(os.environ.get("RADARSENDER_IPK_BUNDLE_BASE", getattr(build, "BUNDLE_BASE", ROOT / "dist" / RELEASE)))
TARGETS = {"mipsel_24kc": ("mipsel", 1, 8), "x86_64": ("x64", 2, 62)}
DEPENDENCIES = {"luci-base", "rpcd", "procd", "jsonfilter", "ubus"}
LICENSES = {
    "radarsender-LICENSE", "upstream-NOTICES-LICENSE", "THIRD_PARTY_NOTICES.md",
    "tcpdump-4.99.7-LICENSE", "libpcap-1.11.0-LICENSE", "musl-arm-LICENSE",
    "gcc-runtime-LICENSE", "gcc-mipsel-LICENSE", "sources.json",
}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def read_tar(data):
    """Read in memory; no archive member is written to the filesystem."""
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = archive.getmembers()
        files = {}
        for member in members:
            if member.isfile():
                files[member.name] = archive.extractfile(member).read()
        return members, files


def normalized(name):
    return name[2:] if name.startswith("./") else name


def control_fields(data):
    """Parse Debian control continuations without using the host opkg."""
    fields, previous = {}, None
    for line in data.decode("utf-8").splitlines():
        if line.startswith((" ", "\t")):
            if previous is None:
                raise ValueError("orphan control continuation")
            fields[previous] += "\n" + line[1:]
        elif line:
            if ":" not in line:
                raise ValueError("invalid control field")
            key, value = line.split(":", 1)
            if key in fields:
                raise ValueError("duplicate control field")
            fields[key] = value.strip()
            previous = key
    return fields


def elf_program_headers(data):
    elfclass = data[4]
    if elfclass == 1:
        offset = struct.unpack_from("<I", data, 28)[0]
        size, count = struct.unpack_from("<HH", data, 42)
        minimum = 32
    else:
        offset = struct.unpack_from("<Q", data, 32)[0]
        size, count = struct.unpack_from("<HH", data, 54)
        minimum = 56
    if size < minimum or offset + size * count > len(data):
        raise ValueError("ELF program headers outside file")
    return [struct.unpack_from("<I", data, offset + index * size)[0] for index in range(count)]


def mips_abi_flags(data):
    offset = struct.unpack_from("<I", data, 32)[0]
    size, count = struct.unpack_from("<HH", data, 46)
    if size < 40 or offset + size * count > len(data):
        raise ValueError("ELF section headers outside file")
    records = []
    for index in range(count):
        header = offset + index * size
        if struct.unpack_from("<I", data, header + 4)[0] == 0x7000002A:
            position, length = struct.unpack_from("<II", data, header + 16)
            if length < 24 or position + length > len(data):
                raise ValueError("MIPS ABI flags outside file")
            records.append(struct.unpack_from("<HBBBBBBIIII", data, position))
    return records


class BuiltIPK(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.release_manifest = json.loads((BUNDLE_BASE / "manifest.json").read_text(encoding="utf-8"))
        cls.release_tar = BUNDLE_BASE / f"luci-app-radarsender_{RELEASE}_universal.tar.gz"
        _, cls.release_files = read_tar(cls.release_tar.read_bytes())

    def package(self, architecture):
        path = OUT / f"luci-app-radarsender_{VERSION}_{architecture}.ipk"
        self.assertTrue(path.is_file(), f"build IPK first: {path}")
        data = path.read_bytes()
        self.assertEqual(data[:2], b"\x1f\x8b", "OpenWrt IPK outer must be gzip tar, not ar")
        members, rawfiles = read_tar(data)
        self.safe_members(members)
        files = {normalized(name): payload for name, payload in rawfiles.items()}
        self.assertEqual({normalized(member.name) for member in members}, {"debian-binary", "control.tar.gz", "data.tar.gz"})
        self.assertEqual(len(members), 3)
        self.assertEqual(len(files), 3)
        for member in members:
            self.assertTrue(member.isfile())
            self.assertEqual((member.uid, member.gid), (0, 0))
            self.assertEqual(member.mode, 0o644)
        self.assertEqual(files["debian-binary"], b"2.0\n")
        return files

    def safe_members(self, members):
        seen = set()
        for member in members:
            name = normalized(member.name)
            if member.isdir() and name in ("", "."):
                continue
            self.assertNotIn(name, seen, f"duplicate tar path: {name}")
            seen.add(name)
            self.assertFalse(name.startswith("/"), name)
            self.assertNotIn("..", PurePosixPath(name).parts, name)
            self.assertNotIn("\\", name)
            self.assertTrue(member.isfile() or member.isdir() or member.issym(), f"unsafe tar type: {name}")
            self.assertEqual((member.uid, member.gid), (0, 0), name)
            self.assertEqual(member.mode & 0o7000, 0, name)
        return seen

    def test_control_metadata_and_scripts(self):
        for architecture in TARGETS:
            with self.subTest(architecture=architecture):
                package = self.package(architecture)
                members, rawfiles = read_tar(package["control.tar.gz"])
                self.safe_members(members)
                files = {normalized(name): data for name, data in rawfiles.items()}
                self.assertEqual(set(files), {"control", "preinst", "postinst", "prerm", "postrm"})
                self.assertTrue(all(member.isfile() for member in members))
                metadata = control_fields(files["control"])
                self.assertEqual(metadata["Package"], "luci-app-radarsender")
                self.assertEqual(metadata["Version"], VERSION)
                self.assertEqual(metadata["Architecture"], architecture)
                dependencies = {re.split(r"\s|\(", dependency.strip(), 1)[0] for dependency in metadata["Depends"].split(",")}
                self.assertEqual(dependencies, DEPENDENCIES)
                self.assertTrue(metadata.get("Description"))
                for member in members:
                    name = normalized(member.name)
                    self.assertEqual(member.mode, 0o644 if name == "control" else 0o755, name)
                for name in ("preinst", "postinst", "prerm", "postrm"):
                    self.assertTrue(files[name].startswith(b"#!/bin/sh\n"), name)
                    self.assertNotIn(b"\r", files[name], name)

    def test_data_paths_modes_licenses_and_release_provenance(self):
        self.assertEqual(self.release_manifest["version"], RELEASE)
        self.assertEqual(set(self.release_manifest["targets"]), {"x64", "x86", "arm64", "arm", "mipsel"})
        self.assertEqual(sha256(self.release_tar.read_bytes()), self.release_manifest["files"][self.release_tar.name])
        expected_common = {name.removeprefix("common/"): data for name, data in self.release_files.items() if name.startswith("common/")}
        for architecture, (target, _, _) in TARGETS.items():
            with self.subTest(architecture=architecture):
                package = self.package(architecture)
                members, rawfiles = read_tar(package["data.tar.gz"])
                self.safe_members(members)
                files = {normalized(name): data for name, data in rawfiles.items()}
                symlinks = {normalized(member.name): member for member in members if member.issym()}
                expected_files = {**expected_common,
                    "usr/sbin/radarsender": self.release_files[f"targets/{target}/radarsender"],
                    "usr/lib/radarsender/tcpdump": self.release_files[f"targets/{target}/tcpdump"],
                }
                self.assertEqual(set(files), set(expected_files), "data payload must contain only real install paths")
                self.assertEqual(set(symlinks), {"usr/libexec/rpcd/radarsender"})
                self.assertEqual(symlinks["usr/libexec/rpcd/radarsender"].linkname, "/usr/sbin/radarsender")
                for name, data in files.items():
                    self.assertEqual(data, expected_files[name], f"changed original release payload: {name}")
                    self.assertFalse(name.startswith(("common/", "targets/")), name)
                    self.assertNotIn("SHA256SUMS", PurePosixPath(name).parts, name)
                    self.assertNotIn("install.sh", PurePosixPath(name).parts, name)
                    self.assertFalse(name.startswith("etc/radarsender/"), name)
                    self.assertFalse(name.endswith("/portable-installed"), name)
                for member in members:
                    name = normalized(member.name)
                    if member.isdir():
                        self.assertEqual(member.mode, 0o755, name)
                    elif member.isfile():
                        executable = name in {"usr/sbin/radarsender", "usr/lib/radarsender/tcpdump", "etc/init.d/radarsender"}
                        self.assertEqual(member.mode, 0o755 if executable else 0o644, name)
                license_files = {name.removeprefix("usr/lib/radarsender/licenses/") for name in files if name.startswith("usr/lib/radarsender/licenses/")}
                self.assertTrue(LICENSES <= license_files)
                sources = json.loads(files["usr/lib/radarsender/licenses/sources.json"])
                self.assertTrue(sources)
                for name, installed in (("radarsender", "usr/sbin/radarsender"), ("tcpdump", "usr/lib/radarsender/tcpdump")):
                    self.assertEqual(sha256(files[installed]), self.release_manifest["files"][f"{target}/{name}"])
                    self.assertEqual(files[installed], (BUNDLE_BASE / target / name).read_bytes())
                self.assertEqual(sha256(files["usr/lib/radarsender/tcpdump"]), self.release_manifest["bundled"]["targets"][target]["sha256"])

    def test_static_elf_and_mipsel_softfloat_capture(self):
        for architecture, (target, elfclass, machine) in TARGETS.items():
            with self.subTest(architecture=architecture):
                _, rawfiles = read_tar(self.package(architecture)["data.tar.gz"])
                files = {normalized(name): data for name, data in rawfiles.items()}
                for name in ("usr/sbin/radarsender", "usr/lib/radarsender/tcpdump"):
                    data = files[name]
                    self.assertGreaterEqual(len(data), 64)
                    self.assertEqual(data[:6], b"\x7fELF" + bytes((elfclass, 1)), name)
                    self.assertEqual(struct.unpack_from("<H", data, 18)[0], machine, name)
                    headers = elf_program_headers(data)
                    self.assertTrue(headers)
                    self.assertNotIn(2, headers, "PT_DYNAMIC is forbidden")
                    self.assertNotIn(3, headers, "PT_INTERP is forbidden")
                    if target == "mipsel":
                        flags = struct.unpack_from("<I", data, 36)[0]
                        self.assertEqual(flags & 0xF000, 0x1000, "MIPS o32 ABI required")
                        self.assertIn(flags & 0xF0000000, (0x50000000, 0x70000000), "MIPS32 / MIPS32r2 required")
                        # Go's soft-float code may still advertise CPR1 size 1;
                        # preserve verified release bytes instead of interpreting
                        # that metadata as actual floating-point instructions.
                        if name.endswith("/tcpdump"):
                            records = mips_abi_flags(data)
                            self.assertEqual(len(records), 1)
                            version, isa, revision, gpr, cpr1, cpr2, fp, *_ = records[0]
                            self.assertEqual(version, 0)
                            self.assertEqual((isa, revision, gpr, cpr1, cpr2, fp), (32, 2, 1, 0, 0, 3), "capture must be MIPS32r2/o32 soft-float (CPR1=0, FP_ABI=soft)")


if __name__ == "__main__":
    unittest.main()
