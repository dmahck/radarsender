# Private static tcpdump

RadarSender bundles tcpdump 4.99.7 and libpcap 1.11.0. Original upstream archives are in `sources/`; upstream URLs and SHA-256 values are pinned in `sources.json`. The archives were downloaded over HTTPS from the projects' official release sites. Original archives remain unchanged; the build applies the recorded compatibility patch below to its temporary source tree.

`bin/<target>/tcpdump` contains validated prebuilt static binaries. `binaries.json` records their architectures, compiler and SHA-256; a new target is recorded only after its build succeeds. The normal release builder verifies every binary against this manifest and rejects ELF dynamic loaders and dynamic dependencies.

## Rebuild

Run from the RadarSender source root:

```sh
python tools/build_tcpdump.py
python tools/build.py
```

The first command requires Python 3, Docker and network access for the compiler image/packages. It prefers the unchanged vendored source archives, checks all input archive SHA-256 values, then compiles in a disposable Debian container. The pinned Zig archive is downloaded only when a Zig target is selected and it is absent. Host source files are mounted read-only. Only generated build artifacts and validated binary outputs are written to the workspace. Optional target arguments are `x64 x86 arm64 arm mipsel`; unknown or repeated target names are rejected before downloading or building.

To add/rebuild only the MT7621 binary without rebuilding the other targets or downloading Zig:

```sh
python tools/build_tcpdump.py mipsel
```

A subset build verifies and retains all unselected manifest entries and binary hashes. Cross-compiler packages are installed only for selected GCC targets. This command still requires Docker and Debian package network access; it is not an offline compiler installation.

- x64: Zig 0.14.1, x86-64 baseline, Linux target 3.2, static musl.
- x86: Zig 0.14.1, Pentium 4, Linux target 3.2, static musl.
- ARM64: Zig 0.14.1, ARMv8-A baseline, Linux target 3.7, static musl.
- ARM: Debian GCC 12 cross compiler, ARMv5TE soft-float, musl 1.2.5 built from the pinned official source. GCC supplies Linux ARMv5 atomic helpers. The Go sender itself requires a supported Linux kernel (3.2+ for this target).
- MIPSEL: Debian GCC 12 `gcc-mipsel-linux-gnu` cross compiler and `linux-libc-dev-mipsel-cross`, MIPS32r2 little-endian o32 soft-float, musl 1.2.5 built from the same pinned official source. This target is intended for Xiaomi Router 3G / MT7621 (`ramips/mt7621`, OpenWrt `mipsel_24kc`), not big-endian MIPS or MIPS64. The wrapper uses musl headers before kernel headers and links static musl, not the Debian glibc sysroot. A floating-point link check rejects an incompatible GCC runtime or a hard-float ABI; these failures must not be hidden with linker mismatch-suppression flags.

The Debian base image is pinned by digest in the build script. Debian build-tool package revisions can advance; binary output hashes are recorded for each completed build, and bit-for-bit reproduction across different package revisions is not claimed.

libpcap retains native Linux Ethernet capture and packet filters. Optional USB, Bluetooth, D-Bus, RDMA, netmap, remote capture, libnl, DAG and SNF support are disabled. tcpdump does not link optional crypto, SMI or capability libraries. `--immediate-mode`, `-U`, `-B` and Ethernet PCAP output remain available. No firmware library or named tcpdump user is required for execution as the root-managed sender service.

`patches/libpcap-ethtool-enotty.patch` treats ENOTTY from three optional SIOCETHTOOL capability queries the same as the existing unsupported-query responses. This keeps default host timestamp capture available with drivers/emulators that do not implement those ioctls. It does not ignore capture socket or permission failures. The unmodified ARM64 binary reproduced a fatal `ETHTOOL_GET_TS_INFO: Not a tty` in QEMU before the patch. Patch hashes are recorded in `binaries.json`.

## Distribution

The installer places the selected binary at `/usr/lib/radarsender/tcpdump`. It preserves the system tcpdump. The accompanying `*-LICENSE` files and `sources.json` are included under `/usr/lib/radarsender/licenses/` in every installation. They cover tcpdump, libpcap, musl, Zig runtime, and the GCC runtime used by the ARM and MIPSEL builds. Despite its historical filename, `musl-arm-LICENSE` is the exact musl 1.2.5 upstream COPYRIGHT and also applies to the MIPSEL build of that same archive. A subset build preserves `gcc-runtime-LICENSE`; the MIPSEL compiler's current Debian notice is exported separately to `artifacts/tcpdump-build/mipsel-gcc-runtime-LICENSE` for provenance review, not silently substituted for the distributed notice.

Build diagnostics are in `artifacts/tcpdump-build/logs/`; `mipsel-toolchain.txt` records the MIPS compiler, flags, multilib selection and ELF floating-point attributes. Release acceptance uses `tests/bundled-tcpdump.sh` and the optional real-Ethernet Go integration test in disposable containers. Release targets are checked for versions, options and offline PCAP roundtrip. Native x64/x86 targets are also checked for live capture. QEMU ARM execution reports `PACKET_ADD_MEMBERSHIP: Protocol not available`, so ARM/ARM64/MIPSEL live capture remains a native-hardware acceptance item. MIPSEL offline execution can use QEMU user-mode, but a successful cross-build or PCAP roundtrip alone is not MT7621 live-capture acceptance. Set `RADARSENDER_LIVE_TARGETS` to choose the native architecture on a suitable test host. Cross-architecture execution does not replace router hardware validation.
