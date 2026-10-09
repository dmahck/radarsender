#!/bin/sh
# Run in the disposable build container created by build_tcpdump.py.
set -eu
[ -f /.dockerenv ] && [ -d /src/third_party/tcpdump ] || exit 1
export DEBIAN_FRONTEND=noninteractive
mkdir -p /work /out/logs
targets=${RADARSENDER_BUILD_TARGETS:-x64 x86 arm64 arm mipsel}
need_zig=0
need_arm=0
need_mipsel=0
seen=' '
for name in $targets; do
    case "$seen" in *" $name "*) echo "Repeated target: $name" >&2; exit 1;; esac
    seen="$seen$name "
    case "$name" in
        x64|x86|arm64) need_zig=1;;
        arm) need_arm=1;;
        mipsel) need_mipsel=1;;
        *) echo "Unknown target: $name" >&2; exit 1;;
    esac
done
packages='make gcc flex bison xz-utils patch libc6-dev'
if [ "$need_arm" = 1 ]; then
    packages="$packages gcc-arm-linux-gnueabi linux-libc-dev-armel-cross"
fi
if [ "$need_mipsel" = 1 ]; then
    packages="$packages gcc-mipsel-linux-gnu linux-libc-dev-mipsel-cross"
fi
echo 'Preparing disposable compiler environment'
(apt-get update -qq && apt-get install -y -qq --no-install-recommends $packages) > /out/logs/build-deps.log 2>&1 || { tail -n 40 /out/logs/build-deps.log; exit 1; }
if [ "$need_zig" = 1 ]; then
    tar -xJf /downloads/zig-x86_64-linux-0.14.1.tar.xz -C /work
    ZIG=/work/zig-x86_64-linux-0.14.1/zig
    export ZIG_GLOBAL_CACHE_DIR=/work/zig-cache
fi
export SOURCE_DATE_EPOCH=0
for name in $targets; do
    case "$name" in
        x64) target=x86_64-linux.3.2-musl; cpu=baseline; host=x86_64-linux-musl;;
        x86) target=x86-linux.3.2-musl; cpu=pentium4; host=i686-linux-musl;;
        arm64) target=aarch64-linux.3.7-musl; cpu=baseline; host=aarch64-linux-musl;;
        arm) target=arm-linux.3.2-musleabi; cpu=arm926ej_s; host=arm-linux-musleabi;;
        mipsel) target=mipsel-linux-musl; cpu=mips32r2-o32-soft-float; host=mipsel-linux-musl;;
        *) echo "Unknown target: $name" >&2; exit 1;;
    esac
    echo "Building static tcpdump for $name ($target, $cpu)"
    mkdir -p "/work/$name" "/out/$name"
    tar -xJf /src/third_party/tcpdump/sources/libpcap-1.11.0.tar.xz -C "/work/$name"
    tar -xJf /src/third_party/tcpdump/sources/tcpdump-4.99.7.tar.xz -C "/work/$name"
    patch --batch --fuzz=0 -d "/work/$name/libpcap-1.11.0" -p1 < /src/third_party/tcpdump/patches/libpcap-ethtool-enotty.patch
    case "$name" in
        x64|x86|arm64)
            export CC="$ZIG cc -target $target -mcpu=$cpu"
            export AR="$ZIG ar" RANLIB="$ZIG ranlib"
            ;;
    esac
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
    elif [ "$name" = mipsel ]; then
        export LDFLAGS='-static -s -Wl,--fatal-warnings'
        # MT7621: MIPS32r2, little-endian o32, no FPU. Build musl ourselves;
        # no glibc from the Debian cross sysroot is linked into the executable.
        tar -xzf /src/third_party/tcpdump/sources/musl-1.2.5.tar.gz -C /work/mipsel
        (
            cd /work/mipsel/musl-1.2.5
            CC=mipsel-linux-gnu-gcc AR=mipsel-linux-gnu-ar RANLIB=mipsel-linux-gnu-ranlib \
                CFLAGS='-Os -march=mips32r2 -mabi=32 -msoft-float -mno-mips16 -fno-ident -ffile-prefix-map=/work=.' LDFLAGS= \
                ./configure --prefix=/work/mipsel/musl-static --target=mipsel-linux-musl --disable-shared &&
            make -j2 && make install
        ) >/out/logs/mipsel-musl.log 2>&1 || { tail -n 60 /out/logs/mipsel-musl.log; exit 1; }
        cat >/work/mipsel/mipsel-musl-gcc <<'EOF'
#!/bin/sh
exec mipsel-linux-gnu-gcc -march=mips32r2 -mabi=32 -msoft-float -mno-mips16 -nostdinc \
    -isystem /usr/lib/gcc-cross/mipsel-linux-gnu/12/include \
    -isystem /usr/lib/gcc-cross/mipsel-linux-gnu/12/include-fixed \
    -isystem /work/mipsel/musl-static/include -isystem /usr/mipsel-linux-gnu/include \
    -B /work/mipsel/musl-static/lib "$@"
EOF
        chmod 755 /work/mipsel/mipsel-musl-gcc
        export CC=/work/mipsel/mipsel-musl-gcc
        export AR=mipsel-linux-gnu-ar RANLIB=mipsel-linux-gnu-ranlib
        {
            mipsel-linux-gnu-gcc --version | head -n 1
            printf 'target: mipsel-linux-musl; ISA: mips32r2; ABI: o32; float: soft\n'
            mipsel-linux-gnu-gcc -march=mips32r2 -mabi=32 -msoft-float -print-multi-lib
            mipsel-linux-gnu-gcc -march=mips32r2 -mabi=32 -msoft-float -print-libgcc-file-name
        } >/out/mipsel-toolchain.txt
        cp /usr/share/doc/gcc-12-mipsel-linux-gnu/copyright /out/mipsel-gcc-runtime-LICENSE
        # Fail clearly before libpcap configure if the cross runtime cannot
        # provide the soft-float helpers. Do not suppress ABI mismatch warnings.
        cat >/work/mipsel/soft-float-check.c <<'EOF'
#include <stdio.h>
volatile double left = 1.25, right = 2.0;
int main(void) { printf("%.2f\n", left * right + left / right); return 0; }
EOF
        "$CC" $CFLAGS $LDFLAGS -o /work/mipsel/soft-float-check /work/mipsel/soft-float-check.c \
            >/out/logs/mipsel-soft-float-check.log 2>&1 || {
                cat /out/logs/mipsel-soft-float-check.log
                echo 'MIPSEL cross compiler must provide a compatible soft-float libgcc.' >&2
                exit 1
            }
        mipsel-linux-gnu-readelf -A /work/mipsel/soft-float-check >>/out/mipsel-toolchain.txt
        # A hard-float program cannot run on MT7621. Reject it even if the
        # linker merely emitted a warning while mixing runtime ABI attributes.
        mipsel-linux-gnu-readelf -A /work/mipsel/soft-float-check | grep -q 'FP ABI: Soft float' || {
            echo 'MIPSEL soft-float ABI verification failed.' >&2; exit 1;
        }
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
    if [ "$name" = mipsel ]; then
        mipsel-linux-gnu-readelf -h -A /out/mipsel/tcpdump >>/out/mipsel-toolchain.txt
        mipsel-linux-gnu-readelf -A /out/mipsel/tcpdump | grep -q 'FP ABI: Soft float' || {
            echo 'MIPSEL tcpdump soft-float ABI verification failed.' >&2; exit 1;
        }
        mipsel-linux-gnu-readelf -h /out/mipsel/tcpdump | grep -q 'o32, mips32r2' || {
            echo 'MIPSEL tcpdump must use MIPS32r2 o32.' >&2; exit 1;
        }
    fi
    echo "Built $name"
done
