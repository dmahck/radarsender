#!/bin/sh
# Build the actual OpenWrt 24.10.0 host opkg; no package-manager test double.
set -eu
[ -f /.dockerenv ] && [ "${RADARSENDER_FIXTURE_CONTAINER:-}" = 1 ] || { echo 'Disposable test container required.' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'Container root required.' >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl zstd cmake gcc make \
    pkg-config libjson-c-dev zlib1g-dev autoconf
for command in curl sha256sum tar zstd cmake make gcc pkg-config; do
    command -v "$command" >/dev/null 2>&1 || { echo "Missing build prerequisite: $command" >&2; exit 1; }
done

# These mirror archives and hashes are pinned by the v24.10.0 package Makefiles:
# https://github.com/openwrt/openwrt/blob/v24.10.0/package/system/opkg/Makefile
# https://github.com/openwrt/openwrt/blob/v24.10.0/package/libs/libubox/Makefile
# The official mirror currently serves .tar.zst, not opkg's old .tar.xz name.
OPKG_COMMIT=38eccbb1fd694d4798ac1baf88f9ba83d1eac616
UBOX_COMMIT=eb9bcb64185ac155c02cc1a604692c4b00368324
OPKG_ARCHIVE=opkg-2024.10.16~38eccbb1.tar.zst
UBOX_ARCHIVE=libubox-2024.03.29~eb9bcb64.tar.zst
OPKG_SHA=de58ff1c99c14789f9ba8946623c8c1e58d022e7e2a659d6f97c6fde54f2c4f4
UBOX_SHA=a4f671d10840fd8487394335636051a6df5edf6d8854af4fcb834a590efb240a
OUT=${RADARSENDER_OPKG_BUILD_ROOT:-/tmp/radarsender-opkg}
case "$OUT" in /tmp/radarsender-opkg|/tmp/radarsender-opkg-*) ;; *) echo 'Host build output must be a dedicated /tmp/radarsender-opkg path.' >&2; exit 1;; esac
mkdir -p "$OUT/bin"
work=$(mktemp -d /tmp/radarsender-opkg-source.XXXXXX)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' HUP INT TERM
for archive in "$OPKG_ARCHIVE" "$UBOX_ARCHIVE"; do
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
        "https://downloads.openwrt.org/sources/$archive" -o "$work/$archive"
done
printf '%s  %s\n%s  %s\n' "$OPKG_SHA" "$work/$OPKG_ARCHIVE" "$UBOX_SHA" "$work/$UBOX_ARCHIVE" | sha256sum -c -
tar --zstd -xf "$work/$UBOX_ARCHIVE" -C "$work"
tar --zstd -xf "$work/$OPKG_ARCHIVE" -C "$work"
jobs=$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '2')
cmake -S "$work/libubox-2024.03.29~eb9bcb64" -B "$work/ubox-build" \
    -DCMAKE_INSTALL_PREFIX="$work/host" -DBUILD_LUA=OFF -DBUILD_EXAMPLES=OFF -DUNIT_TESTING=OFF
cmake --build "$work/ubox-build" --parallel "$jobs"
cmake --install "$work/ubox-build"
# This OpenWrt opkg revision has no built-in SSL dependency. Local-IPK tests do
# not need usign/feed signatures; libubox is statically linked as in host-build.
cmake -S "$work/opkg-2024.10.16~38eccbb1" -B "$work/opkg-build" \
    -DCMAKE_PREFIX_PATH="$work/host" -DCMAKE_C_FLAGS="-I$work/host/include" \
    -DSTATIC_UBOX=ON -DBUILD_TESTS=OFF -DENABLE_USIGN=OFF \
    -DHOST_CPU=x86_64 -DLOCK_FILE=/tmp/opkg.lock \
    -DVERSION="$OPKG_COMMIT (2024-10-16)"
cmake --build "$work/opkg-build" --parallel "$jobs"
cp "$work/opkg-build/src/opkg-cl" "$OUT/bin/opkg"
chmod 755 "$OUT/bin/opkg"
printf 'opkg commit=%s archive=%s sha256=%s\nlibubox commit=%s archive=%s sha256=%s\n' \
    "$OPKG_COMMIT" "$OPKG_ARCHIVE" "$OPKG_SHA" "$UBOX_COMMIT" "$UBOX_ARCHIVE" "$UBOX_SHA" >"$OUT/source-provenance.txt"
cat "$OUT/source-provenance.txt"
"$OUT/bin/opkg" --version
