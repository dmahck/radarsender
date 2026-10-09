#!/usr/bin/env python3
"""Bounded MIPS little-endian runtime checks; QEMU user mode, no installation.

Run on a disposable Linux CI runner with qemu-user and Python 3 installed:
  sudo -n python3 tests/mipsel-runtime.py --bundle dist/0.1.6/mipsel \
    --testbin artifacts/sender-mipsel-tests \
    --upload-testbin artifacts/radarupload-mipsel-tests

All state lives in one temporary directory. No /etc or /usr modifications,
real capture, channel credentials, non-loopback network, or binfmt registration.
The Linux tests use native shell fixtures, not a foreign executable re-exec.
QEMU user-mode does not validate MT7621 PACKET_* socket/live capture behavior.
"""

import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import shlex
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(command, *, timeout=15, env=None, acceptable=(0,)):
    """Bound the whole process group, including native shell test fixtures."""
    process = subprocess.Popen(
        [str(arg) for arg in command], stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, env=env, start_new_session=True,
    )
    try:
        output, _ = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        output, _ = process.communicate()
        raise RuntimeError(f"command exceeded {timeout}s: {command}\n{output.decode(errors='replace')}")
    text = output.decode(errors="replace")
    require(process.returncode in acceptable, f"command exited {process.returncode}: {command}\n{text}")
    return text


def static_mipsel(path):
    data = path.read_bytes()
    require(len(data) >= 52 and data[:6] == b"\x7fELF\x01\x01", f"not ELF32 little-endian: {path}")
    require(struct.unpack_from("<H", data, 18)[0] == 8, f"not MIPS: {path}")
    phoff = struct.unpack_from("<I", data, 28)[0]
    phsize, phcount = struct.unpack_from("<HH", data, 42)
    require(phsize >= 32 and phoff + phsize * phcount <= len(data), f"invalid program headers: {path}")
    for index in range(phcount):
        require(struct.unpack_from("<I", data, phoff + phsize * index)[0] not in (2, 3), f"dynamic dependency/interpreter: {path}")
    require(os.access(path, os.X_OK), f"not executable: {path}")
    print(f"PASS static ELF32 MIPS little-endian: {path.name}", flush=True)


def fixture_pcap():
    """Mixed full Ethernet frames, fixed timestamps, no external fixture input."""
    ethernet = bytes.fromhex("020000000002020000000001")
    unknown = ethernet + bytes.fromhex("88b5") + b"RadarSender MIPS fixture".ljust(46, b"\x00")
    ip4 = bytearray(20)
    ip4[0], ip4[8], ip4[9] = 0x45, 64, 17
    payload = b"mipsel-vlan-fixture"
    struct.pack_into(">H", ip4, 2, 20 + 8 + len(payload))
    ip4[12:20] = bytes.fromhex("c0000201c0000202")
    udp = struct.pack(">HHHH", 40000, 18880, 8 + len(payload), 0) + payload
    vlan = ethernet + bytes.fromhex("810000010800") + ip4 + udp
    ip6 = bytearray(40)
    ip6[0], ip6[6], ip6[7] = 0x60, 6, 64
    struct.pack_into(">H", ip6, 4, 20)
    ip6[8:24] = socket.inet_pton(socket.AF_INET6, "2001:db8::1")
    ip6[24:40] = socket.inet_pton(socket.AF_INET6, "2001:db8::2")
    tcp = bytearray(20)
    struct.pack_into(">HH", tcp, 0, 41000, 18880)
    tcp[12], tcp[13] = 0x50, 0x10
    ipv6 = ethernet + bytes.fromhex("86dd") + ip6 + tcp
    header = struct.pack("<IHHIIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 1)
    return header + b"".join(
        struct.pack("<IIII", 1720000000 + n, 123456, len(frame), len(frame)) + frame
        for n, frame in enumerate((unknown, bytes(vlan), bytes(ipv6)))
    )


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("radarsender", timeout=2)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def rpc(path, method, params=None):
    connection = UnixHTTPConnection(path)
    try:
        payload = json.dumps({"method": method, "params": params or {}})
        connection.request("POST", "/rpc", payload, {"Content-Type": "application/json"})
        response = connection.getresponse()
        require(response.status == 200, f"RPC HTTP status {response.status}")
        data = response.read(65537)
        require(len(data) <= 65536, "oversized fixture RPC response")
        return json.loads(data)
    finally:
        connection.close()


def daemon_fixture(qemu, binary, root, version, env):
    config, state = root / "config", root / "runtime"
    config.mkdir()
    (config / "config.json").write_text("{broken", encoding="utf-8")
    command = [qemu, binary, "serve", "--config-dir", config, "--runtime-dir", state]
    log = root / "daemon.log"
    with log.open("wb") as output:
        process = subprocess.Popen([str(arg) for arg in command], stdout=output, stderr=subprocess.STDOUT, env=env, start_new_session=True)
        control = state / "control.sock"
        try:
            deadline = time.monotonic() + 15
            status = None
            while time.monotonic() < deadline and process.poll() is None:
                if control.exists():
                    try:
                        status = rpc(control, "status")
                        break
                    except (OSError, http.client.HTTPException):
                        pass
                time.sleep(0.05)
            require(status is not None, f"daemon did not become ready: {log.read_text(errors='replace')}")
            require(status.get("ok") is True and status.get("version") == version, f"unexpected status: {status}")
            require(status.get("state") == "idle" and not status.get("capturing") and status.get("config_error"), "damaged-config idle state not reported")
            duplicate = run(command, timeout=10, env=env, acceptable=(1,))
            require("已经运行" in duplicate, f"exclusive process lock failed: {duplicate}")
            require(rpc(control, "configure", {"channel": ""}).get("ok") is True, "fixture config repair failed")
            status = rpc(control, "status")
            require(status.get("state") == "idle" and not status.get("config_error") and not status.get("has_channel"), "config repair did not clear idle error")
            require(json.loads((config / "config.json").read_text()) == {}, "unexpected private fixture configuration")
            for path, mode in ((config, 0o700), (state, 0o700), (config / "config.json", 0o600), (control, 0o600)):
                require(stat.S_IMODE(path.stat().st_mode) == mode, f"incorrect permissions: {path}")
            os.killpg(process.pid, signal.SIGTERM)
            require(process.wait(timeout=20) == 0, f"daemon shutdown failed: {log.read_text(errors='replace')}")
            require(not control.exists(), "daemon left control socket after graceful shutdown")
        finally:
            if process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
    print("PASS MIPS daemon: temporary UNIX RPC, damaged-config repair, lock, permissions, graceful shutdown", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--testbin", type=Path, required=True, help="GOOS=linux GOARCH=mipsle GOMIPS=softfloat internal/sender test binary")
    parser.add_argument("--upload-testbin", type=Path, required=True, help="same target internal/radarupload test binary")
    parser.add_argument("--qemu", default="qemu-mipsel")
    parser.add_argument("--version", default="0.1.6")
    args = parser.parse_args()
    require(sys.platform.startswith("linux"), "Linux runner required")
    require(os.geteuid() == 0, "daemon privilege check requires root; run sudo -n python3 tests/mipsel-runtime.py ... (temporary paths only)")
    require(not Path("/usr/lib/radarsender/tcpdump").exists(), "fixture runner must not have private tcpdump installed")
    qemu = shutil.which(args.qemu)
    require(qemu is not None, "qemu-mipsel unavailable; install qemu-user on disposable runner")
    qemu = str(Path(qemu).resolve())
    bundle = args.bundle.resolve()
    binary, capture = bundle / "radarsender", bundle / "tcpdump"
    testbins = (args.testbin.resolve(), args.upload_testbin.resolve())
    for path in (binary, capture, *testbins):
        static_mipsel(path)
    env = {key: value for key, value in os.environ.items() if not key.startswith("RS_TEST_")}
    require(json.loads(run([qemu, binary, "version"], env=env)).get("version") == args.version, "MIPS CLI version mismatch")
    versions = run([qemu, capture, "--version"], env=env)
    require("tcpdump version 4.99.7" in versions and "libpcap version 1.11.0" in versions, f"bundled capture versions mismatch: {versions}")
    require("--immediate-mode" in run([qemu, capture, "--help"], env=env, acceptable=(0, 1)), "immediate-mode option missing")
    print("PASS MIPS CLI and bundled capture versions/help", flush=True)
    with tempfile.TemporaryDirectory(prefix="radarsender-mipsel-") as temp:
        root = Path(temp)
        source, result = root / "mixed.pcap", root / "roundtrip.pcap"
        source.write_bytes(fixture_pcap())
        run([qemu, capture, "-nn", "--immediate-mode", "-r", source, "-w", result], env=env)
        require(source.read_bytes() == result.read_bytes(), "MIPS offline PCAP roundtrip changed bytes")
        print("PASS MIPS tcpdump offline mixed Ethernet/VLAN/IPv6 exact PCAP roundtrip", flush=True)
        # Native scripts called by Go exec remain host-native; only tcpdump is
        # explicitly launched under QEMU, without relying on host binfmt.
        helpers = root / "bin"
        helpers.mkdir()
        wrapper = helpers / "tcpdump"
        wrapper.write_text(f"#!/bin/sh\nexec {shlex.quote(qemu)} {shlex.quote(str(capture))} \"$@\"\n", encoding="utf-8")
        wrapper.chmod(0o700)
        env["PATH"] = str(helpers) + os.pathsep + env.get("PATH", "/usr/bin:/bin")
        env["TMPDIR"] = str(root)
        for testbin in testbins:
            print(f"Running complete Linux library suite under QEMU: {testbin.name}", flush=True)
            output = run([qemu, testbin, "-test.v", "-test.timeout=180s"], timeout=210, env=env)
            print(output, end="", flush=True)
            require("\nPASS\n" in "\n" + output, f"missing Go PASS marker: {testbin}")
        daemon_fixture(qemu, binary, root, args.version, env)
    print("PASS mipsel QEMU acceptance. MT7621/OpenWrt live PACKET_* capture remains a native hardware check.", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        sys.exit(1)
