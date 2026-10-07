# Private static tcpdump

RadarSender 0.1.3 bundles tcpdump 4.99.7 and libpcap 1.11.0. Original upstream archives are in `sources/`; upstream URLs and SHA-256 values are pinned in `sources.json`. The archives were downloaded over HTTPS from the projects' official release sites. Original archives remain unchanged; the build applies the recorded compatibility patch below to its temporary source tree.

`bin/<target>/tcpdump` contains the four prebuilt static binaries. `binaries.json` records their architectures, compiler and SHA-256. The normal release builder verifies every binary against this manifest and rejects ELF dynamic loaders and dynamic dependencies.

## Rebuild

Run from the RadarSender source root:

```sh
python tools/build_tcpdump.py
python tools/build.py
```

The first command requires Python 3, Docker and network access for the compiler image/packages. It downloads the pinned Zig archive when absent, checks all input archive SHA-256 values, then compiles in a disposable Debian container. Host source files are mounted read-only. Only generated build artifacts and validated binary outputs are written to the workspace. Optional target arguments are `x64 x86 arm64 arm`.

- x64: Zig 0.14.1, x86-64 baseline, Linux target 3.2, static musl.
- x86: Zig 0.14.1, Pentium 4, Linux target 3.2, static musl.
- ARM64: Zig 0.14.1, ARMv8-A baseline, Linux target 3.7, static musl.
- ARM: Debian GCC 12 cross compiler, ARMv5TE soft-float, musl 1.2.5 built from the pinned official source. GCC supplies Linux ARMv5 atomic helpers. The Go sender itself requires a supported Linux kernel (3.2+ for this target).

The Debian base image is pinned by digest in the build script. Debian build-tool package revisions can advance; binary output hashes are recorded for each completed build, and bit-for-bit reproduction across different package revisions is not claimed.

libpcap retains native Linux Ethernet capture and packet filters. Optional USB, Bluetooth, D-Bus, RDMA, netmap, remote capture, libnl, DAG and SNF support are disabled. tcpdump does not link optional crypto, SMI or capability libraries. `--immediate-mode`, `-U`, `-B` and Ethernet PCAP output remain available. No firmware library or named tcpdump user is required for execution as the root-managed sender service.

`patches/libpcap-ethtool-enotty.patch` treats ENOTTY from three optional SIOCETHTOOL capability queries the same as the existing unsupported-query responses. This keeps default host timestamp capture available with drivers/emulators that do not implement those ioctls. It does not ignore capture socket or permission failures. The unmodified ARM64 binary reproduced a fatal `ETHTOOL_GET_TS_INFO: Not a tty` in QEMU before the patch. Patch hashes are recorded in `binaries.json`.

## Distribution

The installer places the selected binary at `/usr/lib/radarsender/tcpdump`. It preserves the system tcpdump. The accompanying `*-LICENSE` files and `sources.json` are included under `/usr/lib/radarsender/licenses/` in every installation. They cover tcpdump, libpcap, musl, Zig runtime, and the GCC runtime used by the ARM build.

Build diagnostics are in `artifacts/tcpdump-build/logs/`. Release acceptance uses `tests/bundled-tcpdump.sh` and the optional real-Ethernet Go integration test in disposable containers. All four targets are checked for versions, options and offline PCAP roundtrip. Native x64/x86 targets are also checked for live capture. QEMU ARM execution reports `PACKET_ADD_MEMBERSHIP: Protocol not available`, so ARM/ARM64 live capture remains a native-hardware acceptance item. Set `RADARSENDER_LIVE_TARGETS` to choose the native architecture on a suitable test host. Cross-architecture execution does not replace router hardware validation.
