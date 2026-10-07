#!/bin/sh
set -eu
umask 077
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
FILES='/usr/sbin/radarsender
/usr/libexec/rpcd/radarsender
/etc/init.d/radarsender
/usr/share/luci/menu.d/luci-app-radarsender.json
/usr/share/rpcd/acl.d/luci-app-radarsender.json
/www/luci-static/resources/view/radarsender/main_v0_1_0.js
/www/luci-static/resources/view/radarsender/main_v0_1_1.js
/www/luci-static/resources/view/radarsender/main_v0_1_2.js
/usr/lib/radarsender/tcpdump
/usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE
/usr/lib/radarsender/licenses/libpcap-1.11.0-LICENSE
/usr/lib/radarsender/licenses/musl-LICENSE
/usr/lib/radarsender/licenses/musl-arm-LICENSE
/usr/lib/radarsender/licenses/gcc-runtime-LICENSE
/usr/lib/radarsender/licenses/zig-LICENSE
/usr/lib/radarsender/licenses/sources.json
/usr/lib/radarsender/install.sh
/usr/lib/radarsender/installed'
fail() { echo "ERROR: $*" >&2; exit 1; }
case "${1:-}" in ''|--check|--uninstall) ;; *) fail 'Supported options: --check, --uninstall';; esac
[ "$(id -u)" = 0 ] || fail 'Run as root on the router.'
if [ "${1:-}" = --uninstall ]; then
    [ -f /usr/lib/radarsender/installed ] || fail 'No standalone installation marker.'
    if [ -x /etc/init.d/radarsender ]; then
        /etc/init.d/radarsender stop || fail 'Could not stop the sender.'
        /etc/init.d/radarsender disable || fail 'Could not disable the sender.'
    fi
    for file in $FILES; do rm -f "$file"; done
    /etc/init.d/rpcd restart || fail 'Files removed, but rpcd restart failed.'
    echo 'RadarSender removed. /etc/radarsender configuration is retained.'
    exit 0
fi
[ -f /etc/openwrt_release ] || fail 'OpenWrt/iStoreOS is required.'
for cmd in jsonfilter ubus sha256sum tar cp chmod mv; do command -v "$cmd" >/dev/null 2>&1 || fail "Missing dependency: $cmd"; done
[ -x /etc/init.d/rpcd ] && [ -d /www/luci-static/resources ] && [ -d /usr/share/luci/menu.d ] && [ -d /usr/share/rpcd/acl.d ] || fail 'LuCI and rpcd are required.'
case "$(uname -m)" in
    x86_64) target=x64;; i?86) target=x86;; aarch64|arm64) target=arm64;; arm*) target=arm;;
    *) fail 'Unsupported CPU. Supported: ARM, ARM64, x86, x64.';;
esac
[ -f "$DIR/SHA256SUMS" ] || fail 'Run the original release installer.'
(cd "$DIR" && sha256sum -c SHA256SUMS >/dev/null) || fail 'Package checksum validation failed.'
BINARY=$DIR/targets/$target/radarsender
CAPTURE=$DIR/targets/$target/tcpdump
[ -x "$BINARY" ] || fail "Target is absent: $target"
"$BINARY" version >/dev/null || fail 'The binary cannot run on this router.'
[ -x "$CAPTURE" ] || fail "Bundled tcpdump is absent: $target"
"$CAPTURE" --version >/dev/null 2>&1 || fail 'Bundled tcpdump cannot run on this router.'
capture_help=$("$CAPTURE" --help 2>&1) || fail 'Bundled tcpdump self-check failed.'
printf '%s\n' "$capture_help" | grep -q -- --immediate-mode || fail 'Bundled tcpdump lacks --immediate-mode.'
if [ ! -f /usr/lib/radarsender/installed ]; then
    for file in $FILES; do [ ! -e "$file" ] && [ ! -L "$file" ] || fail "Refusing to overwrite an unowned file: $file"; done
fi
if [ -x /etc/init.d/radarsender ] && /etc/init.d/radarsender running >/dev/null 2>&1; then
    state=$(/usr/sbin/radarsender call status </dev/null) || fail 'Cannot read sender state.'
    [ "$(jsonfilter -s "$state" -e '@.ok')" = true ] || fail 'Stop the standalone sender before upgrading.'
    case "$(jsonfilter -s "$state" -e '@.state')" in idle|error) ;; *) fail 'Disconnect the standalone sender before upgrading.';; esac
fi
[ "${1:-}" = --check ] && { echo "Preflight passed: $target"; exit 0; }
mkdir /tmp/radarsender-install.lock 2>/dev/null || fail 'Another installation is active; inspect /tmp/radarsender-install.lock.'
backup=$(mktemp -d /tmp/radarsender-backup.XXXXXX) || { rmdir /tmp/radarsender-install.lock; exit 1; }
changed=0; complete=0; was_running=0; was_enabled=0; fresh=0
[ -f /usr/lib/radarsender/installed ] || fresh=1
if [ -x /etc/init.d/radarsender ]; then
    /etc/init.d/radarsender running >/dev/null 2>&1 && was_running=1 || true
    /etc/init.d/radarsender enabled >/dev/null 2>&1 && was_enabled=1 || true
fi
cleanup() {
    code=$?
    trap - EXIT HUP INT TERM
    if [ "$complete" != 1 ] && [ "$changed" = 1 ]; then
        echo 'Install failed; restoring previous standalone files.' >&2
        /etc/init.d/radarsender stop >/dev/null 2>&1 || true
        /etc/init.d/radarsender disable >/dev/null 2>&1 || true
        for file in $FILES; do rm -f "$file"; done
        if [ -s "$backup/list" ] && ! tar -xf "$backup/files.tar" -C /; then
            echo "Restore incomplete. Backup retained at $backup" >&2
            rmdir /tmp/radarsender-install.lock || true
            exit 1
        fi
        restored=1
        [ "$was_enabled" = 0 ] || /etc/init.d/radarsender enable || restored=0
        [ "$was_running" = 0 ] || /etc/init.d/radarsender start || restored=0
        /etc/init.d/rpcd restart >/dev/null 2>&1 || restored=0
        if [ "$restored" != 1 ]; then
            echo "Files restored but service recovery failed. Backup retained at $backup" >&2
            rmdir /tmp/radarsender-install.lock || true
            exit 1
        fi
    fi
    rm -rf "$backup"
    rmdir /tmp/radarsender-install.lock || true
    exit "$code"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
: >"$backup/list"
for file in $FILES; do if [ -e "$file" ] || [ -L "$file" ]; then printf '%s\n' "${file#/}" >>"$backup/list"; fi; done
if [ -s "$backup/list" ]; then tar -cf "$backup/files.tar" -C / -T "$backup/list"; fi
changed=1
[ "$was_running" = 0 ] || /etc/init.d/radarsender stop
for file in $FILES; do mkdir -p "$(dirname "$file")"; done
cp "$BINARY" /usr/sbin/radarsender
chmod 755 /usr/sbin/radarsender
cp "$CAPTURE" /usr/lib/radarsender/tcpdump
chmod 755 /usr/lib/radarsender/tcpdump
cp -R "$DIR/common/." /
chmod 755 /etc/init.d/radarsender
chmod 755 /www/luci-static/resources/view/radarsender /usr/lib/radarsender /usr/lib/radarsender/licenses
for license in "$DIR/common/usr/lib/radarsender/licenses/"*; do chmod 644 "/usr/lib/radarsender/licenses/${license##*/}"; done
chmod 644 /usr/share/luci/menu.d/luci-app-radarsender.json /usr/share/rpcd/acl.d/luci-app-radarsender.json /www/luci-static/resources/view/radarsender/main_v0_1_2.js
ln -sf /usr/sbin/radarsender /usr/libexec/rpcd/radarsender
cp "$DIR/install.sh" /usr/lib/radarsender/install.sh
chmod 755 /usr/lib/radarsender/install.sh
printf '%s\n' 'radarsender-portable-0.1.4' >/usr/lib/radarsender/installed
if [ "$fresh" = 1 ] || [ "$was_enabled" = 1 ]; then /etc/init.d/radarsender enable; fi
# Always run an idle health check, then restore a previously stopped service.
/etc/init.d/radarsender start
healthy=0
for attempt in 1 2 3 4 5; do
    state=$(/usr/sbin/radarsender call status </dev/null 2>/dev/null) || state=
    if [ "$(jsonfilter -s "$state" -e '@.ok' 2>/dev/null)" = true ]; then healthy=1; break; fi
    sleep 1
done
[ "$healthy" = 1 ] || fail 'Service health check failed. See logread -e radarsender.'
if [ "$fresh" = 0 ] && [ "$was_running" = 0 ]; then /etc/init.d/radarsender stop; fi
rm -f /www/luci-static/resources/view/radarsender/main_v0_1_0.js /www/luci-static/resources/view/radarsender/main_v0_1_1.js
rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
/etc/init.d/rpcd restart
complete=1
echo 'RadarSender installed. Refresh LuCI: Services / 雷达发射（独立版）.'
echo 'This is a standalone portable package, not an opkg/apk registration.'
