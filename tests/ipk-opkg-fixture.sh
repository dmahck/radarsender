#!/bin/sh
# Actual OpenWrt 24.10 opkg in a disposable Debian container. Only OpenWrt
# service/rpcd/dependency fixtures are synthetic; IPKs and opkg are unmodified.
set -eu
[ -f /.dockerenv ] && [ "${RADARSENDER_FIXTURE_CONTAINER:-}" = 1 ] || { echo 'Disposable test container required.' >&2; exit 1; }
[ "$(id -u)" = 0 ] && [ "$(uname -m)" = x86_64 ] || { echo 'Disposable x86_64 container root required.' >&2; exit 1; }
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BUILD_ROOT=${RADARSENDER_OPKG_BUILD_ROOT:-/tmp/radarsender-opkg}
OPKG=$BUILD_ROOT/bin/opkg
[ -x "$OPKG" ] || sh "$HERE/build-opkg.sh"
PACKAGE_NAME=luci-app-radarsender
VERSION=0.1.6-1
PACKAGES=${RADARSENDER_TEST_IPK_DIR:-/src/dist/ipk/$VERSION}
NATIVE=$PACKAGES/${PACKAGE_NAME}_${VERSION}_x86_64.ipk
FOREIGN=$PACKAGES/${PACKAGE_NAME}_${VERSION}_mipsel_24kc.ipk
[ -f "$NATIVE" ] && [ -f "$FOREIGN" ] || { echo 'Both release IPKs are required.' >&2; exit 1; }
(cd "$PACKAGES" && sha256sum -c IPK_SHA256SUMS)
fail() { echo "FAIL: $*" >&2; exit 1; }
mkdir -p /etc/init.d /etc/rc.d /etc/opkg /usr/lib/opkg/info /usr/share/luci/menu.d \
    /usr/share/rpcd/acl.d /www/luci-static/resources /usr/libexec/rpcd /usr/sbin
printf 'DISTRIB_RELEASE="24.10.0-fixture"\n' >/etc/openwrt_release
printf 'OLD-SENDER-UNTOUCHED\n' >/usr/sbin/routercapture
cat >/usr/sbin/tcpdump <<'EOF'
#!/bin/sh
# SYSTEM-TCPDUMP-UNTOUCHED
exit 99
EOF
chmod 755 /usr/sbin/tcpdump
system_capture=$(sha256sum /usr/sbin/tcpdump)
original_sender=$(sha256sum /usr/sbin/routercapture)
cat >/usr/bin/jsonfilter <<'EOF'
#!/bin/sh
text=$2; field=${4#@.}
[ "$field" != state ] || [ ! -f /tmp/ipk-fixture-report-sending ] || { echo streaming; exit 0; }
printf '%s' "$text" | sed -n "s/.*\"$field\":\"\{0,1\}\([^\",}]*\).*/\1/p"
EOF
cat >/usr/bin/ubus <<'EOF'
#!/bin/sh
exit 0
EOF
cat >/etc/init.d/rpcd <<'EOF'
#!/bin/sh
printf '%s\n' "${1:-}" >>/tmp/ipk-fixture-rpcd-actions
exit 0
EOF
cat >/etc/rc.common <<'EOF'
#!/bin/sh
set -eu
script=$1; action=$2
printf '%s\n' "$action" >>/tmp/ipk-fixture-service-actions
case "$action" in
    enabled) test -L /etc/rc.d/S96radarsender;;
    enable) ln -sf /etc/init.d/radarsender /etc/rc.d/S96radarsender;;
    disable) rm -f /etc/rc.d/S96radarsender;;
    running) test -S /var/run/radarsender/control.sock;;
    start)
        [ ! -S /var/run/radarsender/control.sock ] || exit 0
        /usr/sbin/radarsender serve >/tmp/ipk-fixture-service.log 2>&1 &
        echo $! >/tmp/ipk-fixture-service.pid
        ;;
    stop)
        if [ -f /tmp/ipk-fixture-service.pid ]; then kill -TERM "$(cat /tmp/ipk-fixture-service.pid)" 2>/dev/null || true; fi
        for i in 1 2 3 4 5; do [ -S /var/run/radarsender/control.sock ] || exit 0; sleep 1; done
        exit 1
        ;;
    *) exit 1;;
esac
EOF
chmod 755 /usr/bin/jsonfilter /usr/bin/ubus /etc/init.d/rpcd /etc/rc.common
cleanup() {
    if [ -f /tmp/ipk-fixture-service.pid ]; then kill -TERM "$(cat /tmp/ipk-fixture-service.pid)" 2>/dev/null || true; fi
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
cat >/etc/opkg.conf <<'EOF'
dest root /
lists_dir ext /var/opkg-lists
arch all 1
arch x86_64 10
EOF
# Dependency status entries are explicitly test doubles; opkg still resolves and
# validates them rather than bypassing dependency checks with force-depends.
seed_dependencies() {
    status=$1
    : >"$status"
    for dependency in luci-base rpcd procd ubus jsonfilter; do
        printf 'Package: %s\nVersion: 1\nArchitecture: all\nStatus: install ok installed\n\n' "$dependency" >>"$status"
    done
}
seed_dependencies /usr/lib/opkg/status
for dependency in luci-base rpcd procd ubus jsonfilter; do : >"/usr/lib/opkg/info/$dependency.list"; done
opkg() { "$OPKG" --conf /etc/opkg.conf "$@"; }

# Prove the real package manager invokes preinst and refuses a portable install
# before replacing any of its existing files.
mkdir -p /usr/lib/radarsender
printf 'portable-owner\n' >/usr/lib/radarsender/installed
printf 'portable-sender-must-not-change\n' >/usr/sbin/radarsender
printf 'portable-capture-must-not-change\n' >/usr/lib/radarsender/tcpdump
portable_before=$(sha256sum /usr/lib/radarsender/installed /usr/sbin/radarsender /usr/lib/radarsender/tcpdump)
if opkg install "$NATIVE" >/tmp/ipk-fixture-portable-reject.log 2>&1; then fail 'preinst accepted portable ownership'; fi
cat /tmp/ipk-fixture-portable-reject.log
grep -q 'Portable RadarSender is installed' /tmp/ipk-fixture-portable-reject.log || fail 'portable rejection did not come from preinst'
[ "$portable_before" = "$(sha256sum /usr/lib/radarsender/installed /usr/sbin/radarsender /usr/lib/radarsender/tcpdump)" ] || fail 'portable installation changed on refusal'
[ ! -e /etc/init.d/radarsender ] || fail 'refused package installed a service'
rm -f /usr/lib/radarsender/installed /usr/sbin/radarsender /usr/lib/radarsender/tcpdump
rmdir /usr/lib/radarsender

opkg install "$NATIVE"
opkg status "$PACKAGE_NAME" | grep -Eq '^Status: install (ok|user) installed$'
opkg status "$PACKAGE_NAME" | grep -q "^Version: $VERSION\$"
opkg files "$PACKAGE_NAME" | tee /tmp/ipk-fixture-files
for file in /usr/sbin/radarsender /usr/lib/radarsender/tcpdump /etc/init.d/radarsender \
    /usr/libexec/rpcd/radarsender /www/luci-static/resources/view/radarsender/main_v0_1_2.js; do
    grep -Fxq "$file" "/usr/lib/opkg/info/$PACKAGE_NAME.list" || fail "opkg did not register $file"
done
[ ! -e /usr/lib/radarsender/installed ] && [ ! -e /usr/lib/radarsender/install.sh ] || fail 'IPK contains portable ownership/uninstaller'
[ "$(/usr/sbin/radarsender version)" = '{"version":"0.1.6"}' ]
/usr/lib/radarsender/tcpdump --version | grep -q 'tcpdump version 4.99.7'
test -L /usr/libexec/rpcd/radarsender
[ "$(readlink /usr/libexec/rpcd/radarsender)" = /usr/sbin/radarsender ]
for file in /usr/sbin/radarsender /usr/lib/radarsender/tcpdump /etc/init.d/radarsender; do [ "$(stat -c %a "$file")" = 755 ]; done
[ "$(stat -c %a /www/luci-static/resources/view/radarsender/main_v0_1_2.js)" = 644 ]
[ "$(stat -c %a /usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE)" = 644 ]
/etc/init.d/radarsender enabled
/etc/init.d/radarsender running
/usr/sbin/radarsender call status </dev/null | grep -q '"ok":true'
printf 'config-retained\n' >/etc/radarsender/retained-ipk-test-marker
config_before=$(find /etc/radarsender -type f -exec sha256sum {} \; | sort)

# Seed only the installed revision metadata as an earlier revision, leaving the
# IPK and all payload files unchanged. This exercises opkg's genuine version
# upgrade path and PKG_UPGRADE, not --force-reinstall (which removes/reinstalls).
# It proves hook lifecycle behavior, not cross-version binary/config migration.
/etc/init.d/radarsender disable
sed -i "/^Package: $PACKAGE_NAME\$/,/^\$/{s/^Version: $VERSION\$/Version: 0.1.6-0/;}" /usr/lib/opkg/status
opkg status "$PACKAGE_NAME" | grep -q '^Version: 0.1.6-0$'
: >/tmp/ipk-fixture-service-actions
# Report a synthetic active session at the state boundary; no capture, channel,
# external receiver or real user credentials are needed for this refusal test.
touch /tmp/ipk-fixture-report-sending
if opkg install "$NATIVE" >/tmp/ipk-fixture-active-reject.log 2>&1; then fail 'active upgrade was accepted'; fi
cat /tmp/ipk-fixture-active-reject.log
grep -q 'Disconnect RadarSender before upgrading' /tmp/ipk-fixture-active-reject.log
if grep -Eq '^(stop|enable|disable)$' /tmp/ipk-fixture-service-actions; then fail 'active-upgrade refusal modified service'; fi
rm /tmp/ipk-fixture-report-sending
opkg install "$NATIVE"
opkg status "$PACKAGE_NAME" | grep -Eq '^Status: install (ok|user) installed$'
opkg status "$PACKAGE_NAME" | grep -q "^Version: $VERSION\$"
if /etc/init.d/radarsender enabled; then fail 'upgrade changed disabled enable state'; fi
if grep -Eq '^(enable|disable)$' /tmp/ipk-fixture-service-actions; then fail 'upgrade hooks changed enable state'; fi
/etc/init.d/radarsender running
/usr/sbin/radarsender call status </dev/null | grep -q '"ok":true'
[ "$config_before" = "$(find /etc/radarsender -type f -exec sha256sum {} \; | sort)" ] || fail 'upgrade changed private configuration'

opkg remove "$PACKAGE_NAME"
if opkg status "$PACKAGE_NAME" | grep -Eq '^Status: install .* installed$'; then fail 'remove left installed status'; fi
for file in /usr/sbin/radarsender /usr/lib/radarsender/tcpdump /etc/init.d/radarsender /usr/libexec/rpcd/radarsender \
    /www/luci-static/resources/view/radarsender/main_v0_1_2.js /usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE; do
    [ ! -e "$file" ] && [ ! -L "$file" ] || fail "package-owned file retained after remove: $file"
done
[ ! -S /var/run/radarsender/control.sock ] && [ ! -L /etc/rc.d/S96radarsender ] || fail 'remove retained active service'
[ "$config_before" = "$(find /etc/radarsender -type f -exec sha256sum {} \; | sort)" ] || fail 'remove changed private configuration'
[ "$system_capture" = "$(sha256sum /usr/sbin/tcpdump)" ] || fail 'system tcpdump changed'
[ "$original_sender" = "$(sha256sum /usr/sbin/routercapture)" ] || fail 'original RouterCapture changed'

# Foreign-architecture staging is a real opkg offline-root install, not a tar
# extraction. No foreign executable or maintainer hook may touch the host.
offline=$(mktemp -d /tmp/radarsender-ipk-offline.XXXXXX)
mkdir -p "$offline/etc/opkg" "$offline/usr/lib/opkg/info"
cat >"$offline/etc/opkg.conf" <<'EOF'
dest root /
lists_dir ext /var/opkg-lists
arch all 1
arch mipsel_24kc 10
EOF
seed_dependencies "$offline/usr/lib/opkg/status"
host_actions=$(sha256sum /tmp/ipk-fixture-service-actions /tmp/ipk-fixture-rpcd-actions)
"$OPKG" --conf "$offline/etc/opkg.conf" --offline-root "$offline" install "$FOREIGN"
"$OPKG" --conf "$offline/etc/opkg.conf" --offline-root "$offline" status "$PACKAGE_NAME" | grep -Eq '^Status: install (ok|user) installed$'
"$OPKG" --conf "$offline/etc/opkg.conf" --offline-root "$offline" status "$PACKAGE_NAME" | grep -q '^Architecture: mipsel_24kc$'
for file in usr/sbin/radarsender usr/lib/radarsender/tcpdump; do
    [ "$(sha256sum "$offline/$file" | awk '{print $1}')" = "$(sha256sum "/src/dist/0.1.6/mipsel/$(basename "$file")" | awk '{print $1}')" ] || fail "offline ELF hash mismatch: $file"
    [ "$(od -An -v -tu1 -N6 "$offline/$file" | xargs)" = '127 69 76 70 1 1' ] || fail "offline executable is not ELF32 little-endian: $file"
done
[ "$host_actions" = "$(sha256sum /tmp/ipk-fixture-service-actions /tmp/ipk-fixture-rpcd-actions)" ] || fail 'offline install executed host hooks'
[ ! -e /usr/sbin/radarsender ] && [ ! -S /var/run/radarsender/control.sock ] || fail 'offline install touched host service'
[ "$config_before" = "$(find /etc/radarsender -type f -exec sha256sum {} \; | sort)" ] || fail 'offline install touched host configuration'
echo 'PASS real OpenWrt 24.10 opkg: portable conflict refusal, dependency resolution, installed status/file ownership/permissions, native service and replacement, remove/config retention, unchanged system capture/old sender, and MIPS offline-root ELF hash with no host hooks'
