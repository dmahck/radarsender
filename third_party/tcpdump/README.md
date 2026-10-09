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

A subset build verifies and retains all unselected manifest entries and binary hashes. Debian ARM cross-compiler packages are installed only for ARM. MIPSEL alone downloads the fixed OpenWrt compiler archive and installs zstd for extraction; the compiler archive stays in build artifacts, not vendored sources or release packages. This command still requires Docker and build-tool package network access; it is not an offline compiler installation.

- x64: Zig 0.14.1, x86-64 baseline, Linux target 3.2, static musl.
- x86: Zig 0.14.1, Pentium 4, Linux target 3.2, static musl.
- ARM64: Zig 0.14.1, ARMv8-A baseline, Linux target 3.7, static musl.
- ARM: Debian GCC 12 cross compiler, ARMv5TE soft-float, musl 1.2.5 built from the pinned official source. GCC supplies Linux ARMv5 atomic helpers. The Go sender itself requires a supported Linux kernel (3.2+ for this target).
- MIPSEL: fixed OpenWrt 24.10.0 `ramips/mt7621` Linux-x86_64 toolchain with GCC 13.3.0 and a genuine soft-float GCC runtime. The compiler archive URL/SHA-256 and GCC source URL/hash are pinned in `sources.json`. musl 1.2.5 is rebuilt from the same unchanged upstream archive used for ARM. This target is MIPS32r2 little-endian o32 soft-float, intended for Xiaomi Router 3G / MT7621 (OpenWrt `mipsel_24kc`), not big-endian MIPS or MIPS64. The wrapper preserves the OpenWrt target specs, uses explicit compiler/fresh-musl headers, searches kernel headers afterward, and uses `-B` to prioritize the freshly built static musl CRT and libc. Applying a second generic musl specs file to the OpenWrt wrapper was rejected during validation because it broke MIPS target option expansion. Static non-PIE link checks treat linker warnings as fatal and reject hard-float ABI output; no mismatch-suppression flags are used. Debian's default MIPS GCC 12 CRT/libgcc were confirmed hard-float and are not used for this target.

The Debian base image is pinned by digest in the build script. Debian build-tool package revisions can advance; binary output hashes are recorded for each completed build, and bit-for-bit reproduction across different package revisions is not claimed.

libpcap retains native Linux Ethernet capture and packet filters. Optional USB, Bluetooth, D-Bus, RDMA, netmap, remote capture, libnl, DAG and SNF support are disabled. tcpdump does not link optional crypto, SMI or capability libraries. `--immediate-mode`, `-U`, `-B` and Ethernet PCAP output remain available. No firmware library or named tcpdump user is required for execution as the root-managed sender service.

`patches/libpcap-ethtool-enotty.patch` treats ENOTTY from three optional SIOCETHTOOL capability queries the same as the existing unsupported-query responses. This keeps default host timestamp capture available with drivers/emulators that do not implement those ioctls. It does not ignore capture socket or permission failures. The unmodified ARM64 binary reproduced a fatal `ETHTOOL_GET_TS_INFO: Not a tty` in QEMU before the patch. Patch hashes are recorded in `binaries.json`.

## Distribution

The installer places the selected binary at `/usr/lib/radarsender/tcpdump`. It preserves the system tcpdump. The accompanying `*-LICENSE` files and `sources.json` are included under `/usr/lib/radarsender/licenses/` in every installation. They cover tcpdump, libpcap, musl, Zig runtime, and the GCC runtime used by the ARM and MIPSEL builds. Despite its historical filename, `musl-arm-LICENSE` is the exact musl 1.2.5 upstream COPYRIGHT and also applies to the MIPSEL build of that same archive. A subset build preserves ARM's `gcc-runtime-LICENSE`; `gcc-mipsel-LICENSE` supplies the GCC 13.3.0 runtime license and exception with source/provenance references. The OpenWrt compiler archive itself is not redistributed in RadarSender packages.

Build diagnostics are in `artifacts/tcpdump-build/logs/`; `mipsel-toolchain.txt` records compiler `--version`/`-v`, target, multilib/runtime selection and ELF floating-point attributes. `mipsel-soft-float-check` is a build-only static executable for QEMU validation; its expected output is exactly `3.12`, and it is not installed. Release acceptance uses `tests/bundled-tcpdump.sh` and the optional real-Ethernet Go integration test in disposable containers. Release targets are checked for versions, options and offline PCAP roundtrip. Native x64/x86 targets are also checked for live capture. QEMU ARM execution reports `PACKET_ADD_MEMBERSHIP: Protocol not available`, so ARM/ARM64/MIPSEL live capture remains a native-hardware acceptance item. MIPSEL offline execution can use QEMU user-mode, but a successful cross-build or PCAP roundtrip alone is not MT7621 live-capture acceptance. Set `RADARSENDER_LIVE_TARGETS` to choose the native architecture on a suitable test host. Cross-architecture execution does not replace router hardware validation.
