"""Build private static tcpdump binaries from pinned upstream sources using Docker."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
VENDOR = ROOT / "third_party" / "tcpdump"
DOWNLOADS = ROOT / "artifacts" / "downloads"
BUILD = ROOT / "artifacts" / "tcpdump-build"
IMAGE = "debian@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251"
TARGET_CPUS = {
    "x64": "x86-64 baseline",
    "x86": "Pentium 4",
    "arm64": "ARMv8-A baseline",
    "arm": "ARMv5TE soft-float",
    "mipsel": "MIPS32r2 little-endian o32 soft-float",
}
ZIG_TARGETS = {"x64", "x86", "arm64"}
GCC_TARGETS = {"arm", "mipsel"}


def parse_targets(args):
    targets = args or list(TARGET_CPUS)
    if any(target not in TARGET_CPUS for target in targets):
        raise SystemExit("Targets: " + " ".join(TARGET_CPUS))
    if len(targets) != len(set(targets)):
        raise SystemExit("Each target may be selected only once")
    return targets


def verify_archive(path, expected):
    if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
        raise RuntimeError(f"Checksum mismatch: {path.name}")


def prepare_sources(sources, targets):
    required = {"tcpdump", "libpcap"}
    if set(targets) & GCC_TARGETS:
        required.add("musl")
    if set(targets) & ZIG_TARGETS:
        required.add("zig")
    if "mipsel" in targets:
        required.add("openwrt-toolchain")
    DOWNLOADS.mkdir(parents=True, exist_ok=True)
    for component, spec in sources.items():
        if component not in required:
            continue
        path = DOWNLOADS / spec["url"].rsplit("/", 1)[-1]
        vendored = VENDOR / "sources" / path.name
        if not path.exists():
            if vendored.is_file():
                verify_archive(vendored, spec["sha256"])
                shutil.copy2(vendored, path)
            else:
                print("Downloading", component, spec["version"], flush=True)
                partial = path.with_name(path.name + ".download")
                try:
                    with urllib.request.urlopen(spec["url"], timeout=120) as response, partial.open("wb") as out:
                        shutil.copyfileobj(response, out)
                    verify_archive(partial, spec["sha256"])
                    partial.replace(path)
                finally:
                    if partial.exists():
                        partial.unlink()
        verify_archive(path, spec["sha256"])
        if component not in ("zig", "openwrt-toolchain"):
            vendored.parent.mkdir(exist_ok=True)
            if vendored.is_file():
                verify_archive(vendored, spec["sha256"])
            else:
                shutil.copy2(path, vendored)

def main():
    targets = parse_targets(sys.argv[1:])
    sources = json.loads((VENDOR / "sources.json").read_text(encoding="utf-8"))
    previous = json.loads((VENDOR / "binaries.json").read_text(encoding="utf-8"))
    if any(target not in TARGET_CPUS for target in previous["targets"]):
        raise RuntimeError("Unknown target in existing binary manifest")
    prepare_sources(sources, targets)
    BUILD.mkdir(parents=True, exist_ok=True)
    from build import check_static_elf, TARGETS
    # A subset rebuild must not silently change or drop an unselected binary.
    for target, spec in previous["targets"].items():
        if target not in targets:
            data = (VENDOR / "bin" / target / "tcpdump").read_bytes()
            check_static_elf(data, TARGETS[target][2], TARGETS[target][3])
            if hashlib.sha256(data).hexdigest() != spec["sha256"]:
                raise RuntimeError(f"Existing binary checksum mismatch: {target}")
    subprocess.run([
        "docker", "run", "--rm", "--platform", "linux/amd64",
        "-e", "RADARSENDER_BUILD_TARGETS=" + " ".join(targets),
        "--mount", f"type=bind,source={ROOT},target=/src,readonly",
        "--mount", f"type=bind,source={DOWNLOADS},target=/downloads,readonly",
        "--mount", f"type=bind,source={BUILD},target=/out", IMAGE,
        "sh", "/src/tools/build_tcpdump.sh",
    ], check=True)
    for target in targets:
        data = (BUILD / target / "tcpdump").read_bytes()
        check_static_elf(data, TARGETS[target][2], TARGETS[target][3])
        dest = VENDOR / "bin" / target
        dest.mkdir(parents=True, exist_ok=True)
        (dest / "tcpdump").write_bytes(data)
        os.chmod(dest / "tcpdump", 0o755)
    # Preserve the ARM GCC 12 notice; MIPSEL has its own GCC 13 runtime notice.
    if "arm" in targets:
        if not (VENDOR / "gcc-runtime-LICENSE").is_file():
            raise RuntimeError("Missing GCC runtime license")
    if "mipsel" in targets:
        if not (VENDOR / "gcc-mipsel-LICENSE").is_file():
            raise RuntimeError("Missing MIPSEL GCC runtime license")
    compiler = "Zig " + sources["zig"]["version"] + "; ARM: GCC 12 + musl " + sources["musl"]["version"]
    if (VENDOR / "bin" / "mipsel" / "tcpdump").is_file():
        toolchain = sources["openwrt-toolchain"]
        compiler += "; MIPSEL: OpenWrt " + toolchain["version"] + " / " + toolchain["compiler"] + " + musl " + sources["musl"]["version"]
    manifest={"tcpdump":sources["tcpdump"]["version"],"libpcap":sources["libpcap"]["version"],"compiler":compiler,"linkage":"static musl","targets":{}}
    manifest["patches"]={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted((VENDOR/"patches").glob("*.patch"))}
    for target, cpu in TARGET_CPUS.items():
        path=VENDOR/"bin"/target/"tcpdump"
        if path.is_file():
            data=path.read_bytes()
            check_static_elf(data,TARGETS[target][2],TARGETS[target][3])
            manifest["targets"][target]={"cpu":cpu,"sha256":hashlib.sha256(data).hexdigest()}
    (VENDOR/"binaries.json").write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
    print("Verified static tcpdump:", ", ".join(targets), flush=True)

if __name__ == "__main__":
    main()
