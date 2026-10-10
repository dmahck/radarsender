#!/bin/sh
set -eu
[ -f /.dockerenv ] && [ "${RADARSENDER_FIXTURE_CONTAINER:-}" = 1 ] || { echo 'Disposable test container required.'; exit 1; }
BASE=/src/dist/0.1.7
BIN=$BASE/x64/radarsender
PACKAGE=$BASE/luci-app-radarsender_0.1.7_universal.run
[ "$("$BIN" version)" = '{"version":"0.1.7"}' ]
sh -n /src/tools/install.sh
sh "$PACKAGE" --verify
cp "$PACKAGE" /tmp/corrupt.run
printf X >>/tmp/corrupt.run
if sh /tmp/corrupt.run --verify >/tmp/corrupt.log 2>&1; then echo 'FAIL: corrupt package accepted'; exit 1; fi
grep -q 'integrity check failed' /tmp/corrupt.log
for arch in arm arm64 x86 x64 mipsel; do
    mkdir /tmp/bundle-$arch
    tar -xzf "$BASE/$arch/radarsender-0.1.7-$arch.tar.gz" -C /tmp/bundle-$arch
    (cd /tmp/bundle-$arch && sha256sum -c SHA256SUMS >/dev/null)
done
mkdir -p /etc/radarsender
printf '{broken' >/etc/radarsender/config.json
"$BIN" serve >/tmp/sender.log 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true' EXIT
ready=0
for i in 1 2 3 4 5; do
    state=$("$BIN" call status </dev/null)
    if printf '%s' "$state" | grep -q '"config_error"'; then ready=1; break; fi
    sleep 1
done
[ "$ready" = 1 ]
printf '%s' "$state" | grep -q '"ok":true'
printf '%s' "$state" | grep -Fq '"version":"0.1.7"'
if "$BIN" serve >/tmp/duplicate.log 2>&1; then echo 'FAIL: duplicate service started'; exit 1; fi
printf '%s' '{"channel":""}' | "$BIN" call configure | grep -q '"ok":true'
state=$("$BIN" call status </dev/null)
printf '%s' "$state" | grep -q '"state":"idle"'
if printf '%s' "$state" | grep -q '"config_error"'; then echo 'FAIL: config not repaired'; exit 1; fi
[ "$(stat -c %a /etc/radarsender/config.json)" = 600 ]
[ "$(stat -c %a /var/run/radarsender/control.sock)" = 600 ]
if grep -q 'max_rate_kbps\|interface' /etc/radarsender/config.json; then echo 'FAIL: obsolete controls persisted'; exit 1; fi
kill -TERM "$pid"
wait "$pid"
trap - EXIT
[ ! -e /var/run/radarsender/control.sock ]
echo 'PASS package hashes, five target archives, corrupt rejection, damaged-config daemon startup, exclusive process lock, RPC config repair, private permissions, graceful shutdown'
