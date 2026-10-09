"""Build real opkg IPKs from validated 0.1.6 standalone bundle bytes."""
import argparse
import gzip
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path
import tarfile

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("radarsender_portable_build", ROOT / "tools" / "build.py")
build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build)
VERSION = "0.1.6"
PACKAGE_VERSION = VERSION + "-2"
PACKAGE = "luci-app-radarsender"
BUNDLE_BASE = Path(os.environ.get("RADARSENDER_IPK_BUNDLE_BASE", ROOT / "dist" / VERSION))
OUT = ROOT / "dist" / "ipk" / PACKAGE_VERSION
TARGETS = {"mipsel": "mipsel_24kc", "x64": "x86_64"}


def tar_gz(entries):
    """Deterministic OpenWrt tar members; no portable wrapper or root hashes."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        directories = set()
        for name in entries:
            parts = name.split("/")
            directories.update("/".join(parts[:n]) for n in range(1, len(parts)))
        for name in sorted(directories):
            info = tarfile.TarInfo("./" + name)
            info.type, info.mode, info.mtime = tarfile.DIRTYPE, 0o755, 0
            tar.addfile(info)
        for name, (data, mode, link) in sorted(entries.items()):
            if name.startswith("/") or ".." in name.split("/"):
                raise RuntimeError("Unsafe package member")
            info = tarfile.TarInfo("./" + name)
            info.mode, info.mtime = mode, 0
            if link:
                info.type, info.linkname = tarfile.SYMTYPE, link
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    return gzip.compress(buf.getvalue(), compresslevel=9, mtime=0)


def bundle_entries(base, target):
    manifest = json.loads((base / "manifest.json").read_text(encoding="utf-8"))
    if manifest["version"] != VERSION:
        raise RuntimeError("Bundle version mismatch")
    archive_path = base / target / f"radarsender-{VERSION}-{target}.tar.gz"
    archive_bytes = archive_path.read_bytes()
    if build.digest(archive_bytes) != manifest["files"][archive_path.relative_to(base).as_posix()]:
        raise RuntimeError("Bundle archive checksum mismatch")
    result = {}
    with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:gz") as tar:
        files = {}
        for member in tar.getmembers():
            if not member.isfile() or member.name.startswith("/") or ".." in member.name.split("/"):
                raise RuntimeError("Unexpected bundle archive member")
            files[member.name] = (tar.extractfile(member).read(), member.mode)
        for line in files["SHA256SUMS"][0].decode().splitlines():
            expected, name = line.split("  ", 1)
            if build.digest(files[name][0]) != expected:
                raise RuntimeError("Inner bundle checksum mismatch")
        for name, (data, mode) in files.items():
            if name.startswith("common/"):
                result[name[len("common/"):]] = (data, mode, None)
        for name, destination in (("radarsender", "usr/sbin/radarsender"), ("tcpdump", "usr/lib/radarsender/tcpdump")):
            data = files[f"targets/{target}/{name}"][0]
            _, _, elfclass, machine = build.TARGETS[target]
            build.check_static_elf(data, elfclass, machine)
            if build.digest(data) != manifest["files"][f"{target}/{name}"]:
                raise RuntimeError("Binary does not match release manifest")
            result[destination] = (data, 0o755, None)
    result["usr/libexec/rpcd/radarsender"] = (b"", 0o777, "/usr/sbin/radarsender")
    # A portable installer must never own or uninstall opkg-managed files.
    if any(n.startswith("etc/radarsender/") or n.endswith("/installed") or n.endswith("/install.sh") for n in result):
        raise RuntimeError("Configuration or portable ownership marker in IPK")
    return result


def scripts(target, entries):
    # opkg validates the package's Architecture before invoking these hooks.
    # Do not embed portable ELF detection: minimal OpenWrt may lack od entirely.
    preinst = f'''#!/bin/sh
set -eu
fail() {{ echo "ERROR: $*" >&2; exit 1; }}
[ -z "${{IPKG_INSTROOT:-}}" ] || exit 0
[ ! -e /usr/lib/radarsender/installed ] || fail 'Portable RadarSender is installed. Disconnect and uninstall it first; configuration will be retained.'
for cmd in jsonfilter ubus; do command -v "$cmd" >/dev/null 2>&1 || fail "Missing dependency: $cmd"; done
[ -x /etc/init.d/rpcd ] && [ -d /www/luci-static/resources ] || fail 'LuCI and rpcd are required.'
owned=/usr/lib/opkg/info/{PACKAGE}.list
for file in {" ".join('/' + n for n in sorted(entries))}; do
    if [ -e "$file" ] || [ -L "$file" ]; then
        [ -f "$owned" ] && grep -Fxq "$file" "$owned" || fail "Refusing to overwrite an unowned file: $file"
    fi
done
if [ -x /etc/init.d/radarsender ]; then
    if /etc/init.d/radarsender running >/dev/null 2>&1; then
        state=$(/usr/sbin/radarsender call status </dev/null) || fail 'Cannot read sender state.'
        [ "$(jsonfilter -s "$state" -e '@.ok')" = true ] || fail 'Cannot confirm sender state.'
        case "$(jsonfilter -s "$state" -e '@.state')" in idle|error) ;; *) fail 'Disconnect RadarSender before upgrading.';; esac
        /etc/init.d/radarsender stop || fail 'Cannot stop sender.'
    fi
fi
exit 0
'''
    postinst = '''#!/bin/sh
set -eu
[ -z "${IPKG_INSTROOT:-}" ] || exit 0
/usr/sbin/radarsender version >/dev/null
/usr/lib/radarsender/tcpdump --version >/dev/null 2>&1
[ "${PKG_UPGRADE:-0}" = 1 ] || /etc/init.d/radarsender enable
/etc/init.d/radarsender start
healthy=0
for attempt in 1 2 3 4 5; do
    state=$(/usr/sbin/radarsender call status </dev/null 2>/dev/null) || state=
    if [ "$(jsonfilter -s "$state" -e '@.ok' 2>/dev/null)" = true ]; then healthy=1; break; fi
    sleep 1
done
[ "$healthy" = 1 ] || { echo 'RadarSender installed but daemon health check failed; see logread -e radarsender.' >&2; exit 1; }
rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
/etc/init.d/rpcd restart
echo 'RadarSender installed. Refresh LuCI: Services / 雷达发射（独立版）.'
'''
    prerm = '''#!/bin/sh
set -eu
[ -z "${IPKG_INSTROOT:-}" ] || exit 0
if [ -x /etc/init.d/radarsender ]; then
    # Use OpenWrt's normal upgrade lifecycle: stop the old daemon. This opkg
    # revision can rewrite the old owner list after an aborted upgrade, so do
    # not add an active-session refusal in prerm after ownership has moved.
    /etc/init.d/radarsender stop
    # Preserve enable state while opkg replaces this package during upgrades.
    [ "${PKG_UPGRADE:-0}" = 1 ] || /etc/init.d/radarsender disable
fi
'''
    postrm = '''#!/bin/sh
set -eu
[ -z "${IPKG_INSTROOT:-}" ] || exit 0
# /etc/radarsender is private runtime state, never package-owned or removed.
rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
if [ -x /etc/init.d/rpcd ]; then /etc/init.d/rpcd restart; fi
'''
    return {name: (script.encode(), 0o755, None) for name, script in (('preinst', preinst), ('postinst', postinst), ('prerm', prerm), ('postrm', postrm))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle-base", type=Path, default=BUNDLE_BASE)
    args = parser.parse_args()
    base = args.bundle_base.resolve()
    OUT.mkdir(parents=True, exist_ok=True)
    hashes = []
    for target, architecture in TARGETS.items():
        entries = bundle_entries(base, target)
        control = f'''Package: {PACKAGE}
Version: {PACKAGE_VERSION}
Architecture: {architecture}
Maintainer: dmahck
Section: luci
Priority: optional
Depends: luci-base, rpcd, procd, jsonfilter, ubus
Source: https://github.com/dmahck/radarsender
License: MIT
Installed-Size: {sum(len(data) for data, _, link in entries.values() if not link)}
Description: Standalone realtime Ethernet PCAP sender with LuCI and private static tcpdump.
 Runtime configuration is root-private and retained on removal.
'''
        controls = scripts(target, entries)
        controls["control"] = (control.encode(), 0o644, None)
        package = tar_gz({"debian-binary": (b"2.0\n", 0o644, None), "control.tar.gz": (tar_gz(controls), 0o644, None), "data.tar.gz": (tar_gz(entries), 0o644, None)})
        path = OUT / f"{PACKAGE}_{PACKAGE_VERSION}_{architecture}.ipk"
        path.write_bytes(package)
        hashes.append(f"{hashlib.sha256(package).hexdigest()}  {path.name}\n")
        print(path)
    (OUT / "IPK_SHA256SUMS").write_text("".join(hashes), encoding="ascii", newline="\n")


if __name__ == "__main__":
    main()
