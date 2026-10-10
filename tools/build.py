"""Build standalone static OpenWrt bundles; no downloads or old-project imports."""
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
VERSION = "0.1.7"
OUT = ROOT / "dist" / VERSION
VENDOR = ROOT / "third_party" / "tcpdump"
TARGETS = {
    "x64": ("amd64", {"GOAMD64": "v1"}, 2, 62),
    "x86": ("386", {"GO386": "softfloat"}, 1, 3),
    "arm64": ("arm64", {"GOARM64": "v8.0"}, 2, 183),
    "arm": ("arm", {"GOARM": "5,softfloat"}, 1, 40),
    "mipsel": ("mipsle", {"GOMIPS": "softfloat"}, 1, 8),
}

def digest(data):
    return hashlib.sha256(data).hexdigest()

def check_static_elf(data, elfclass, machine):
    assert data[:4]==b"\x7fELF" and data[4]==elfclass and data[5]==1 and struct.unpack_from("<H",data,18)[0]==machine, "Wrong ELF architecture"
    phoff=struct.unpack_from("<Q" if elfclass==2 else "<I",data,32 if elfclass==2 else 28)[0]
    entsize,count=struct.unpack_from("<HH",data,54 if elfclass==2 else 42)
    assert all(struct.unpack_from("<I",data,phoff+i*entsize)[0] not in (2,3) for i in range(count)), "Dynamic ELF dependency or interpreter"

def archive(entries):
    entries = dict(entries)
    sums = "".join(f"{digest(data)}  {name}\n" for name, (data, _) in sorted(entries.items()))
    entries["SHA256SUMS"] = (sums.encode(), 0o644)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for name, (data, mode) in sorted(entries.items()):
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime = len(data), mode, 0
            tar.addfile(info, io.BytesIO(data))
    return gzip.compress(buf.getvalue(), compresslevel=9, mtime=0)

def run_wrapper(payload):
    header = r'''#!/bin/sh
set -eu
umask 077
case "${1:-}" in ''|--verify|--check) ;; *) echo 'Options: --verify, --check' >&2; exit 2;; esac
tmp=$(mktemp -d /tmp/radarsender-unpack.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' HUP INT TERM
line=$(awk '/^__RADARSENDER_PAYLOAD__$/ { print NR+1; exit }' "$0")
tail -n +"$line" "$0" >"$tmp/payload.tar.gz"
actual=$(sha256sum "$tmp/payload.tar.gz" | awk '{print $1}')
[ "$actual" = '@HASH@' ] || { echo 'Package integrity check failed.' >&2; exit 1; }
mkdir "$tmp/package"
tar -xzf "$tmp/payload.tar.gz" -C "$tmp/package"
(cd "$tmp/package" && sha256sum -c SHA256SUMS >/dev/null)
if [ "${1:-}" = --verify ]; then echo 'All package checksums verified.'; exit 0; fi
sh "$tmp/package/install.sh" "$@"
exit $?
__RADARSENDER_PAYLOAD__
'''
    return header.replace("@HASH@", digest(payload)).encode() + payload

def main():
    OUT.mkdir(parents=True, exist_ok=True)
    vendor_manifest=json.loads((VENDOR/"binaries.json").read_text(encoding="utf-8"))
    patches={p.name:digest(p.read_bytes()) for p in sorted((VENDOR/"patches").glob("*.patch"))}
    assert vendor_manifest.get("patches")==patches, "Rebuild tcpdump after changing compatibility patches"
    common = {"install.sh": ((ROOT/"tools/install.sh").read_bytes().replace(b"\r\n", b"\n"), 0o755)}
    for name, target in (("LICENSE", "radarsender-LICENSE"), ("THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md")):
        common["common/usr/lib/radarsender/licenses/"+target] = ((ROOT/name).read_bytes(), 0o644)
    for file in sorted(VENDOR.glob("*-LICENSE")):
        common["common/usr/lib/radarsender/licenses/"+file.name]=(file.read_bytes(),0o644)
    common["common/usr/lib/radarsender/licenses/sources.json"]=((VENDOR/"sources.json").read_bytes(),0o644)
    for file in sorted((ROOT/"openwrt/root").rglob("*")):
        if file.is_file():
            name = file.relative_to(ROOT/"openwrt/root").as_posix()
            common["common/"+name] = (file.read_bytes().replace(b"\r\n", b"\n"), 0o755 if name.startswith("etc/init.d/") else 0o644)
    universal = dict(common)
    for name, (arch, tuning, elfclass, machine) in TARGETS.items():
        folder = OUT/name
        folder.mkdir(exist_ok=True)
        binary = folder/"radarsender"
        env = {k:v for k,v in os.environ.items() if k not in ("GOARM", "GOARM64", "GOAMD64", "GO386", "GOMIPS", "GOMIPS64", "GOEXPERIMENT")}
        env.update(GOOS="linux", GOARCH=arch, CGO_ENABLED="0", **tuning)
        subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", str(binary), "./cmd/radarsender"], cwd=ROOT, env=env, check=True)
        data=binary.read_bytes()
        check_static_elf(data,elfclass,machine)
        tcpdump=(VENDOR/"bin"/name/"tcpdump").read_bytes()
        assert digest(tcpdump)==vendor_manifest["targets"][name]["sha256"], "Bundled tcpdump checksum mismatch"
        check_static_elf(tcpdump,elfclass,machine)
        (folder/"tcpdump").write_bytes(tcpdump)
        os.chmod(folder/"tcpdump", 0o755)
        entry={f"targets/{name}/radarsender":(data,0o755),f"targets/{name}/tcpdump":(tcpdump,0o755)}
        universal.update(entry)
        target_payload = archive({**common,**entry})
        (folder/f"radarsender-{VERSION}-{name}.tar.gz").write_bytes(target_payload)
        (folder/f"luci-app-radarsender_{VERSION}_{name}.run").write_bytes(run_wrapper(target_payload))
    payload=archive(universal)
    base=f"luci-app-radarsender_{VERSION}_universal"
    (OUT/(base+".tar.gz")).write_bytes(payload)
    (OUT/(base+".run")).write_bytes(run_wrapper(payload))
    manifest={"version":VERSION,"package":"radarsender-portable","source":"standalone Go module","toolchain":subprocess.check_output(["go","version"],text=True).strip(),"targets":list(TARGETS),"dependencies":["LuCI","rpcd","procd","jsonfilter","ubus"],"bundled":vendor_manifest,"files":{f.relative_to(OUT).as_posix():digest(f.read_bytes()) for f in sorted(OUT.rglob("*")) if f.is_file() and f.name not in ("SHA256SUMS","manifest.json")}}
    (OUT/"manifest.json").write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    sums="".join(f"{digest(f.read_bytes())}  {f.relative_to(OUT).as_posix()}\n" for f in sorted(OUT.rglob("*")) if f.is_file() and f.name!="SHA256SUMS")
    (OUT/"SHA256SUMS").write_text(sums,encoding="utf-8")
    print(OUT)

if __name__=="__main__":
    main()
