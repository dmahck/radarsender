"""Read-only installer architecture checks using real POSIX sh and od.

Fixtures replace uname and redirect od to temporary ELF headers. No host router
paths are changed, and every command that could mutate an installation is blocked.
"""
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "tools" / "install.sh"


def find_shell():
    if os.name != "nt":
        return shutil.which("sh")
    # Do not accidentally select Windows' bash.exe (a WSL launcher).
    git = shutil.which("git")
    if git:
        git_root = Path(git).resolve().parent.parent
        for relative in ("usr/bin/sh.exe", "bin/sh.exe"):
            candidate = git_root / relative
            if candidate.is_file():
                return str(candidate)
    return None


def shell_path(path):
    path = Path(path).resolve()
    if os.name == "nt":
        return "/" + path.drive[0].lower() + path.as_posix()[2:]
    return str(path)


def elf_header(elf_class=1, byte_order=1, machine=8, version=1):
    header = bytearray(64 if elf_class == 2 else 52)
    header[:4] = b"\x7fELF"
    header[4:7] = bytes((elf_class, byte_order, version))
    order = "<" if byte_order == 1 else ">"
    struct.pack_into(order + "H", header, 18, machine)
    struct.pack_into(order + "I", header, 20, 1)
    return bytes(header)


class ArchitectureSelection(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.shell = find_shell()
        if cls.shell is None:
            if os.name == "nt":
                raise unittest.SkipTest("Git for Windows POSIX sh is required")
            raise RuntimeError("POSIX sh is required")

    def select(self, machine, busybox=None, shell=None):
        with tempfile.TemporaryDirectory(prefix="radarsender-arch-") as tmp:
            directory = Path(tmp)
            environment = os.environ.copy()
            for name, data in (("BUSYBOX", busybox), ("SHELL", shell)):
                fixture = directory / (name.lower() + ".elf")
                if data is not None:
                    fixture.write_bytes(data)
                environment["RADARSENDER_TEST_" + name] = shell_path(fixture)
            environment.update(
                RADARSENDER_TEST_MACHINE=machine,
                RADARSENDER_TEST_READS=shell_path(directory / "reads"),
                RADARSENDER_TEST_BLOCKED=shell_path(directory / "blocked"),
            )
            scripts = {
                "uname": """#!/bin/sh
[ "$#" = 1 ] && [ "$1" = -m ] || exit 97
printf '%s\\n' "$RADARSENDER_TEST_MACHINE"
""",
                "od": """#!/bin/sh
[ "$#" = 5 ] && [ "$1 $2 $3 $4" = '-An -v -tu1 -N20' ] || exit 97
printf '%s\\n' "$5" >>"$RADARSENDER_TEST_READS"
case "$5" in
    /bin/busybox) fixture=$RADARSENDER_TEST_BUSYBOX;;
    /bin/sh) fixture=$RADARSENDER_TEST_SHELL;;
    *) exit 97;;
esac
exec "$RADARSENDER_TEST_REAL_OD" "$1" "$2" "$3" "$4" "$fixture"
""",
            }
            blocked = """#!/bin/sh
printf 'unexpected command: %s\\n' "$0" >>"$RADARSENDER_TEST_BLOCKED"
exit 97
"""
            for name in ("id", "mkdir", "mktemp", "rm", "rmdir", "cp", "mv",
                         "chmod", "ln", "tar", "sha256sum", "jsonfilter", "ubus"):
                scripts[name] = blocked
            for name, content in scripts.items():
                script = directory / name
                script.write_text(content, encoding="utf-8", newline="\n")
                script.chmod(0o755)
            result = subprocess.run(
                [self.shell, "-c", """
PATH="$3:$PATH"
export PATH
RADARSENDER_TEST_REAL_OD=$(command -v od) || exit 98
export RADARSENDER_TEST_REAL_OD
PATH="$1:$PATH"
export PATH
exec sh "$2" --print-target
""", "architecture-fixture", shell_path(directory), shell_path(INSTALLER),
                 shell_path(Path(self.shell).parent)],
                env=environment, capture_output=True, text=True, timeout=10,
            )
            self.assertNotEqual(result.returncode, 98, "real od is required")
            self.assertFalse((directory / "blocked").exists(),
                             "--print-target must not require root or mutate an installation")
            reads = (directory / "reads").read_text().splitlines() if (directory / "reads").exists() else []
            return result, reads

    def assert_rejected(self, machine, busybox=None, shell=None):
        result, reads = self.select(machine, busybox, shell)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(result.stdout, "")
        self.assertIn("ERROR:", result.stderr)
        return reads

    def test_existing_architectures_do_not_probe_mips_elf(self):
        for machine, target in (("x86_64", "x64"), ("i386", "x86"),
                                ("i686", "x86"), ("aarch64", "arm64"),
                                ("arm64", "arm64"), ("armv7l", "arm")):
            with self.subTest(machine=machine):
                result, reads = self.select(machine)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, target + "\n")
                self.assertEqual(reads, [])

    def test_mt7621_uname_labels_require_little_endian_mips_elf(self):
        for machine in ("mips", "mipsel", "mipsle"):
            with self.subTest(machine=machine):
                result, reads = self.select(machine, elf_header())
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "mipsel\n")
                self.assertEqual(reads, ["/bin/busybox"])

    def test_big_endian_rejected_even_for_mipsel_uname(self):
        for machine in ("mips", "mipsel", "mipsle"):
            with self.subTest(machine=machine):
                self.assert_rejected(machine, elf_header(byte_order=2))

    def test_elf64_wrong_machine_and_invalid_version_rejected(self):
        for header in (elf_header(elf_class=2), elf_header(machine=62),
                       elf_header(version=0), elf_header(byte_order=0)):
            with self.subTest(header=header[:20]):
                self.assert_rejected("mips", header)

    def test_shell_script_busybox_falls_back_to_actual_shell_elf(self):
        result, reads = self.select("mips", b"#!/bin/sh\necho fixture\n", elf_header())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "mipsel\n")
        self.assertEqual(reads, ["/bin/busybox", "/bin/sh"])

    def test_missing_busybox_falls_back_to_actual_shell_elf(self):
        result, reads = self.select("mips", None, elf_header())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "mipsel\n")
        self.assertEqual(reads, ["/bin/busybox", "/bin/sh"])

    def test_incompatible_busybox_is_not_hidden_by_compatible_shell(self):
        reads = self.assert_rejected("mipsel", elf_header(byte_order=2), elf_header())
        self.assertEqual(reads, ["/bin/busybox"])

    def test_unrecognized_or_missing_userland_rejected(self):
        self.assert_rejected("mips", b"#!/bin/sh\n", b"#!/bin/sh\n")
        self.assert_rejected("mips")

    def test_truncated_recognizable_elf_rejected(self):
        reads = self.assert_rejected("mips", elf_header()[:19], elf_header())
        self.assertEqual(reads, ["/bin/busybox"])

    def test_mips64_and_unknown_uname_labels_rejected_without_probe(self):
        for machine in ("mips64", "mips64el", "riscv64", "unknown"):
            with self.subTest(machine=machine):
                self.assertEqual(self.assert_rejected(machine, elf_header()), [])


if __name__ == "__main__":
    unittest.main()
