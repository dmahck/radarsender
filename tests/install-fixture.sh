#!/bin/sh
# Disposable-container test doubles for OpenWrt init/rpcd, not real firmware.
set -eu
[ -f /.dockerenv ] && [ "${RADARSENDER_FIXTURE_CONTAINER:-}" = 1 ] || { echo 'Disposable test container required.'; exit 1; }
PACKAGE=${RADARSENDER_TEST_PACKAGE:-/src/dist/0.1.6/luci-app-radarsender_0.1.6_universal.run}
mkdir -p /etc/init.d /etc/rc.d /usr/share/luci/menu.d /usr/share/rpcd/acl.d /www/luci-static/resources /usr/libexec/rpcd
printf 'fixture-only\n' >/etc/openwrt_release
printf 'OLD-SENDER-UNTOUCHED\n' >/usr/sbin/routercapture
cat >/usr/bin/jsonfilter <<'EOF'
#!/bin/sh
text=$2; field=${4#@.}
printf '%s' "$text" | sed -n "s/.*\"$field\":\"\{0,1\}\([^\",}]*\).*/\1/p"
EOF
cat >/usr/bin/ubus <<'EOF'
#!/bin/sh
exit 0
EOF
cat >/etc/init.d/rpcd <<'EOF'
#!/bin/sh
if [ -f /tmp/fail-rpcd-once ]; then rm /tmp/fail-rpcd-once; exit 1; fi
exit 0
EOF
cat >/etc/rc.common <<'EOF'
#!/bin/sh
set -eu
script=$1; action=$2
case "$action" in
    enabled) test -L /etc/rc.d/S96radarsender;;
    enable) ln -sf /etc/init.d/radarsender /etc/rc.d/S96radarsender;;
    disable) rm -f /etc/rc.d/S96radarsender;;
    running) test -S /var/run/radarsender/control.sock;;
    start)
        [ ! -S /var/run/radarsender/control.sock ] || exit 0
        /usr/sbin/radarsender serve >/tmp/fixture-service.log 2>&1 &
        echo $! >/tmp/fixture-service.pid
        ;;
    stop)
        if [ -f /tmp/fixture-service.pid ]; then kill -TERM "$(cat /tmp/fixture-service.pid)" 2>/dev/null || true; fi
        for i in 1 2 3 4 5; do [ -S /var/run/radarsender/control.sock ] || exit 0; sleep 1; done
        exit 1
        ;;
    *) exit 1;;
esac
EOF
chmod 755 /usr/bin/jsonfilter /usr/bin/ubus /etc/init.d/rpcd /etc/rc.common
if command -v tcpdump >/dev/null 2>&1; then echo 'FAIL: fixture must start without system tcpdump'; exit 1; fi
sh "$PACKAGE" --check
sh "$PACKAGE"
[ "$(/usr/sbin/radarsender version)" = '{"version":"0.1.6"}' ]
[ -f /usr/lib/radarsender/installed ]
test -L /usr/libexec/rpcd/radarsender
/usr/sbin/radarsender call status </dev/null | grep -q '"ok":true'
[ "$(stat -c %a /usr/lib/radarsender/tcpdump)" = 755 ]
/usr/lib/radarsender/tcpdump --version | grep -q 'tcpdump version 4.99.7'
[ -s /usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE ]
[ -s /usr/lib/radarsender/licenses/libpcap-1.11.0-LICENSE ]
[ -s /usr/lib/radarsender/licenses/radarsender-LICENSE ]
[ -s /usr/lib/radarsender/licenses/upstream-NOTICES-LICENSE ]
[ -s /usr/lib/radarsender/licenses/THIRD_PARTY_NOTICES.md ]
[ -s /usr/lib/radarsender/licenses/musl-LICENSE ]
[ -s /usr/lib/radarsender/licenses/zig-LICENSE ]
[ "$(stat -c %a /usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE)" = 644 ]
[ -s /usr/lib/radarsender/licenses/musl-arm-LICENSE ]
[ -s /usr/lib/radarsender/licenses/gcc-runtime-LICENSE ]
# A system copy may subsequently be installed by other software. Keep it intact.
cat >/usr/sbin/tcpdump <<'EOF'
#!/bin/sh
# SYSTEM-TCPDUMP-UNTOUCHED
[ "${1:-}" = --help ] && { echo '--immediate-mode'; exit 0; }
exit 99
EOF
chmod 755 /usr/sbin/tcpdump
system_capture=$(sha256sum /usr/sbin/tcpdump)
[ "$(stat -c %a /www/luci-static/resources/view/radarsender)" = 755 ]
[ "$(stat -c %a /www/luci-static/resources/view/radarsender/main_v0_1_2.js)" = 644 ]
# Preserve a previous file verbatim when a post-install restart fails.
printf '\n/* previous-release-marker */\n' >>/www/luci-static/resources/view/radarsender/main_v0_1_2.js
before=$(sha256sum /www/luci-static/resources/view/radarsender/main_v0_1_2.js)
# Rollback must restore private binaries as well as the page.
printf '\nprevious-private-binary\n' >>/usr/lib/radarsender/tcpdump
previous_capture=$(sha256sum /usr/lib/radarsender/tcpdump)
printf '\nprevious-license-marker\n' >>/usr/lib/radarsender/licenses/upstream-NOTICES-LICENSE
previous_license=$(sha256sum /usr/lib/radarsender/licenses/upstream-NOTICES-LICENSE)
touch /tmp/fail-rpcd-once
if sh "$PACKAGE"; then echo 'FAIL: injected restart failure ignored'; exit 1; fi
[ "$before" = "$(sha256sum /www/luci-static/resources/view/radarsender/main_v0_1_2.js)" ]
[ "$previous_capture" = "$(sha256sum /usr/lib/radarsender/tcpdump)" ]
[ "$previous_license" = "$(sha256sum /usr/lib/radarsender/licenses/upstream-NOTICES-LICENSE)" ]
[ "$system_capture" = "$(sha256sum /usr/sbin/tcpdump)" ]
[ ! -d /tmp/radarsender-install.lock ]
# Preserve an intentionally stopped/disabled standalone installation.
/etc/init.d/radarsender stop
/etc/init.d/radarsender disable
sh "$PACKAGE"
if /etc/init.d/radarsender enabled || /etc/init.d/radarsender running; then echo 'FAIL: stopped service unexpectedly enabled'; exit 1; fi
printf 'config-retained\n' >/etc/radarsender/retained-test-marker
sh /usr/lib/radarsender/install.sh --uninstall
[ ! -e /usr/sbin/radarsender ]
[ ! -e /usr/lib/radarsender/tcpdump ]
[ ! -e /usr/lib/radarsender/licenses/tcpdump-4.99.7-LICENSE ]
[ ! -e /usr/lib/radarsender/licenses/radarsender-LICENSE ]
[ ! -e /usr/lib/radarsender/licenses/upstream-NOTICES-LICENSE ]
[ ! -e /usr/lib/radarsender/licenses/THIRD_PARTY_NOTICES.md ]
[ "$system_capture" = "$(sha256sum /usr/sbin/tcpdump)" ]
[ -f /etc/radarsender/retained-test-marker ]
[ "$(cat /usr/sbin/routercapture)" = OLD-SENDER-UNTOUCHED ]
echo 'PASS isolated installer fixture: install without system tcpdump, private binary and license permissions, injected-failure rollback, disabled-state preservation, uninstall/config retention, system tcpdump and original sender preservation'

# Optional cross-version upgrade verification against the 0.1.2 pre-bundled baseline.
if [ -n "${RADARSENDER_PREVIOUS_PACKAGE:-}" ]; then
    sh "$RADARSENDER_PREVIOUS_PACKAGE"
    printf '%s' '{"interface":"eth0","channel":"http://192.0.2.1#fixture","max_rate_kbps":128}' | /usr/sbin/radarsender call configure | grep -q '"ok":true'
    previous=$(sha256sum /www/luci-static/resources/view/radarsender/main_v0_1_2.js)
    touch /tmp/fail-rpcd-once
    if sh "$PACKAGE"; then echo 'FAIL: cross-version injected failure ignored'; exit 1; fi
    [ "$previous" = "$(sha256sum /www/luci-static/resources/view/radarsender/main_v0_1_2.js)" ]
    [ ! -e /usr/lib/radarsender/tcpdump ]
    [ "$(/usr/sbin/radarsender version)" = '{"version":"0.1.2"}' ]
    sh "$PACKAGE"
    [ "$(/usr/sbin/radarsender version)" = '{"version":"0.1.6"}' ]
    /usr/lib/radarsender/tcpdump --version | grep -q 'tcpdump version 4.99.7'
    grep -q main_v0_1_2 /usr/share/luci/menu.d/luci-app-radarsender.json
    /usr/sbin/radarsender call status </dev/null | grep -q '"has_channel":true'
    printf '%s' '{"channel":""}' | /usr/sbin/radarsender call configure | grep -q '"ok":true'
    if grep -q 'max_rate_kbps\|interface' /etc/radarsender/config.json; then echo 'FAIL: obsolete legacy settings retained'; exit 1; fi
    /usr/sbin/radarsender call status </dev/null | grep -q '"has_channel":true'
    sh /usr/lib/radarsender/install.sh --uninstall
    [ ! -e /www/luci-static/resources/view/radarsender/main_v0_1_2.js ]
    [ ! -e /usr/lib/radarsender/tcpdump ]
    [ "$system_capture" = "$(sha256sum /usr/sbin/tcpdump)" ]
    echo 'PASS 0.1.2 to 0.1.6 upgrade: rollback restores old binary and removes new private tcpdump; successful upgrade preserves channel and system tcpdump'
fi
