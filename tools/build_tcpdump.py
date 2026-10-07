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

def main():
    sources = json.loads((VENDOR / "sources.json").read_text(encoding="utf-8"))
    DOWNLOADS.mkdir(parents=True, exist_ok=True)
    BUILD.mkdir(parents=True, exist_ok=True)
    for component, spec in sources.items():
        path = DOWNLOADS / spec["url"].rsplit("/", 1)[-1]
        if not path.exists():
            print("Downloading", component, spec["version"], flush=True)
            with urllib.request.urlopen(spec["url"], timeout=120) as response, path.open("wb") as out:
                shutil.copyfileobj(response, out)
        if hashlib.sha256(path.read_bytes()).hexdigest() != spec["sha256"]:
            raise RuntimeError(f"Checksum mismatch: {path.name}")
        if component != "zig":
            (VENDOR / "sources").mkdir(exist_ok=True)
            shutil.copy2(path, VENDOR / "sources" / path.name)
    targets = sys.argv[1:] or ["x64", "x86", "arm64", "arm"]
    if any(t not in ("x64", "x86", "arm64", "arm") for t in targets):
        raise SystemExit("Targets: x64 x86 arm64 arm")
    subprocess.run([
        "docker", "run", "--rm", "--platform", "linux/amd64",
        "-e", "RADARSENDER_BUILD_TARGETS=" + " ".join(targets),
        "--mount", f"type=bind,source={ROOT},target=/src,readonly",
        "--mount", f"type=bind,source={DOWNLOADS},target=/downloads,readonly",
        "--mount", f"type=bind,source={BUILD},target=/out", IMAGE,
        "sh", "/src/tools/build_tcpdump.sh",
    ], check=True)
    from build import check_static_elf, TARGETS
    for target in targets:
        data = (BUILD / target / "tcpdump").read_bytes()
        check_static_elf(data, TARGETS[target][2], TARGETS[target][3])
        dest = VENDOR / "bin" / target
        dest.mkdir(parents=True, exist_ok=True)
        (dest / "tcpdump").write_bytes(data)
        os.chmod(dest / "tcpdump", 0o755)
    if "arm" in targets:
        shutil.copy2(BUILD/"gcc-runtime-LICENSE",VENDOR/"gcc-runtime-LICENSE")
    manifest={"tcpdump":sources["tcpdump"]["version"],"libpcap":sources["libpcap"]["version"],"compiler":"Zig " + sources["zig"]["version"] + "; ARM: GCC 12 + musl " + sources["musl"]["version"],"linkage":"static musl","targets":{}}
    manifest["patches"]={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted((VENDOR/"patches").glob("*.patch"))}
    for target, cpu in (("x64","x86-64 baseline"),("x86","Pentium 4"),("arm64","ARMv8-A baseline"),("arm","ARMv5TE soft-float")):
        path=VENDOR/"bin"/target/"tcpdump"
        if path.is_file():
            data=path.read_bytes()
            check_static_elf(data,TARGETS[target][2],TARGETS[target][3])
            manifest["targets"][target]={"cpu":cpu,"sha256":hashlib.sha256(data).hexdigest()}
    (VENDOR/"binaries.json").write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
    print("Verified static tcpdump:", ", ".join(targets), flush=True)

if __name__ == "__main__":
    main()
