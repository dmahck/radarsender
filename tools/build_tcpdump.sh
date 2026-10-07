#!/bin/sh
# Run in the disposable build container created by build_tcpdump.py.
set -eu
[ -f /.dockerenv ] && [ -d /src/third_party/tcpdump ] || exit 1
export DEBIAN_FRONTEND=noninteractive
mkdir -p /work /out/logs
echo 'Preparing disposable compiler environment'
(apt-get update -qq && apt-get install -y -qq --no-install-recommends make gcc flex bison xz-utils patch libc6-dev gcc-arm-linux-gnueabi linux-libc-dev-armel-cross) > /out/logs/build-deps.log 2>&1 || { tail -n 40 /out/logs/build-deps.log; exit 1; }
tar -xJf /downloads/zig-x86_64-linux-0.14.1.tar.xz -C /work
ZIG=/work/zig-x86_64-linux-0.14.1/zig
export ZIG_GLOBAL_CACHE_DIR=/work/zig-cache
export SOURCE_DATE_EPOCH=0
for name in ${RADARSENDER_BUILD_TARGETS:-x64 x86 arm64 arm}; do
    case "$name" in
        x64) target=x86_64-linux.3.2-musl; cpu=baseline; host=x86_64-linux-musl;;
        x86) target=x86-linux.3.2-musl; cpu=pentium4; host=i686-linux-musl;;
        arm64) target=aarch64-linux.3.7-musl; cpu=baseline; host=aarch64-linux-musl;;
        arm) target=arm-linux.3.2-musleabi; cpu=arm926ej_s; host=arm-linux-musleabi;;
        *) echo "Unknown target: $name" >&2; exit 1;;
    esac
    echo "Building static tcpdump for $name ($target, $cpu)"
    mkdir -p "/work/$name" "/out/$name"
    tar -xJf /src/third_party/tcpdump/sources/libpcap-1.11.0.tar.xz -C "/work/$name"
    tar -xJf /src/third_party/tcpdump/sources/tcpdump-4.99.7.tar.xz -C "/work/$name"
    patch --batch --fuzz=0 -d "/work/$name/libpcap-1.11.0" -p1 < /src/third_party/tcpdump/patches/libpcap-ethtool-enotty.patch
    export CC="$ZIG cc -target $target -mcpu=$cpu"
    export AR="$ZIG ar" RANLIB="$ZIG ranlib"
    export CFLAGS='-Os -fno-ident -ffile-prefix-map=/work=. '
    export LDFLAGS='-static -s'
    if [ "$name" = arm ]; then
        # GCC supplies the Linux ARMv5 atomic helpers missing in Zig's runtime.
        tar -xzf /src/third_party/tcpdump/sources/musl-1.2.5.tar.gz -C /work/arm
        (
            cd /work/arm/musl-1.2.5
            CC=arm-linux-gnueabi-gcc AR=arm-linux-gnueabi-ar RANLIB=arm-linux-gnueabi-ranlib \
                CFLAGS='-Os -march=armv5te -mfloat-abi=soft -fno-ident -ffile-prefix-map=/work=.' LDFLAGS= \
                ./configure --prefix=/work/arm/musl-static --target=arm-linux-musleabi --disable-shared &&
            make -j2 && make install
        ) >/out/logs/arm-musl.log 2>&1 || { tail -n 60 /out/logs/arm-musl.log; exit 1; }
        cat >/work/arm/arm-musl-gcc <<'EOF'
#!/bin/sh
exec arm-linux-gnueabi-gcc -march=armv5te -mfloat-abi=soft -nostdinc \
    -isystem /usr/lib/gcc-cross/arm-linux-gnueabi/12/include \
    -isystem /usr/lib/gcc-cross/arm-linux-gnueabi/12/include-fixed \
    -isystem /work/arm/musl-static/include -isystem /usr/arm-linux-gnueabi/include \
    -B /work/arm/musl-static/lib "$@"
EOF
        chmod 755 /work/arm/arm-musl-gcc
        export CC=/work/arm/arm-musl-gcc
        export AR=arm-linux-gnueabi-ar RANLIB=arm-linux-gnueabi-ranlib
        arm-linux-gnueabi-gcc --version | head -n 1 >/out/arm-toolchain.txt
        cp /usr/share/doc/gcc-12-arm-linux-gnueabi/copyright /out/gcc-runtime-LICENSE
    fi
    (
        cd "/work/$name/libpcap-1.11.0"
        ./configure --build=x86_64-pc-linux-gnu --host="$host" --with-pcap=linux \
            --disable-shared --disable-usb --disable-bluetooth --disable-dbus \
            --disable-rdma --disable-netmap --disable-remote --without-libnl \
            --without-dag --without-snf &&
        make -j2 libpcap.a
    ) >"/out/logs/$name-libpcap.log" 2>&1 || {
        cp "/work/$name/libpcap-1.11.0/config.log" "/out/logs/$name-libpcap-config.log"
        tail -n 60 "/out/logs/$name-libpcap.log"; exit 1;
    }
    (
        cd "/work/$name/tcpdump-4.99.7"
        ./configure --build=x86_64-pc-linux-gnu --host="$host" \
            --without-crypto --without-smi --without-cap-ng --without-sandbox-capsicum &&
        make -j2 tcpdump &&
        cp tcpdump "/out/$name/tcpdump"
    ) >"/out/logs/$name-tcpdump.log" 2>&1 || {
        cp "/work/$name/tcpdump-4.99.7/config.log" "/out/logs/$name-tcpdump-config.log"
        tail -n 60 "/out/logs/$name-tcpdump.log"; exit 1;
    }
    echo "Built $name"
done
