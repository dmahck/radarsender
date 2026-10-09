#!/bin/sh
# Local loopback traffic only; run in a fresh disposable Linux container.
set -eu
[ -f /.dockerenv ] && [ "${RADARSENDER_FIXTURE_CONTAINER:-}" = 1 ] || exit 1
BASE=/src/dist/0.1.6
for arch in x64 x86 arm64 arm mipsel; do
    capture=$BASE/$arch/tcpdump
    "$capture" --version >"/tmp/$arch-version.txt" 2>&1
    grep -q 'tcpdump version 4.99.7' "/tmp/$arch-version.txt"
    grep -q 'libpcap version 1.11.0' "/tmp/$arch-version.txt"
    "$capture" --help 2>&1 | grep -q -- --immediate-mode
    "$capture" -nn --immediate-mode -r /src/tests/fixtures/ethernet.pcap -w "/tmp/$arch-offline.pcap" 2>"/tmp/$arch-offline.log"
    cmp /src/tests/fixtures/ethernet.pcap "/tmp/$arch-offline.pcap"
    echo "PASS $arch: private static binary, versions, immediate mode, exact PCAP roundtrip"
done
# Foreign architectures in this optional script require registered binfmt.
# CI uses explicit qemu-mipsel in mipsel-runtime.py instead.
# Run live tests only for architectures supported by the native kernel.
# QEMU user-mode cannot translate all PACKET_* socket operations.
for arch in ${RADARSENDER_LIVE_TARGETS:-x64 x86}; do
    capture=$BASE/$arch/tcpdump
    # No network access is needed: all traffic and captures stay on loopback.
    timeout 10 "$capture" -i lo -nn -s 65535 -B 2048 -U --immediate-mode -c 1 -w "/tmp/$arch-live.pcap" icmp 2>"/tmp/$arch-live.log" &
    pid=$!
    trap 'kill "$pid" 2>/dev/null || true' EXIT
    ready=0
    for i in 1 2 3 4 5; do
        if grep -q 'listening on lo' "/tmp/$arch-live.log"; then ready=1; break; fi
        sleep 1
    done
    [ "$ready" = 1 ] || { cat "/tmp/$arch-live.log"; exit 1; }
    ping -c 1 -W 1 127.0.0.1 >/dev/null
    wait "$pid"
    trap - EXIT
    "$capture" -nn -r "/tmp/$arch-live.pcap" 2>/dev/null | grep -q 'ICMP echo request'
    echo "PASS $arch: real loopback capture"
done
echo "Live capture tested for: ${RADARSENDER_LIVE_TARGETS:-x64 x86}. Other targets require native hardware validation."
