# Third-party notices

The project's MIT license applies only to original RadarSender
code. It does **not** replace the licenses of the files under `third_party/`,
their source archives, or the bundled tcpdump executables.

## Included components

| Component | Version / origin | License and notice files |
| --- | --- | --- |
| tcpdump | 4.99.7, official upstream release archive | BSD-style; `third_party/tcpdump/tcpdump-4.99.7-LICENSE` and `upstream-NOTICES-LICENSE` |
| libpcap | 1.11.0, official upstream release archive | BSD-style; `third_party/tcpdump/libpcap-1.11.0-LICENSE` and `upstream-NOTICES-LICENSE` |
| musl | 1.2.5 for ARM and MIPSEL; musl supplied by Zig for the other targets | MIT and additional component notices; `musl-arm-LICENSE` and `musl-LICENSE` |
| Zig runtime | Zig 0.14.1 toolchain | MIT; `zig-LICENSE` |
| GCC runtime | GCC 12 ARM; OpenWrt 24.10.0 GCC 13.3.0 MIPSEL soft-float | GPL with the GCC Runtime Library Exception; `gcc-runtime-LICENSE` and `gcc-mipsel-LICENSE` |

All license-file paths in the last three rows are relative to
`third_party/tcpdump/`. Refer to each full notice for its applicable terms.
GCC's runtime exception is included in the supplied file; the build uses an
eligible GCC compilation process for the independent tcpdump/libpcap program.
This does not relicense the GCC runtime itself.
The historical `musl-arm-LICENSE` filename contains the full upstream
musl 1.2.5 COPYRIGHT; it also covers MIPSEL built from the identical pinned
archive. MIPSEL subset builds preserve ARM's existing GCC notice and use
the separate GCC 13.3.0 runtime notice. The OpenWrt compiler archive itself
is downloaded only for builds, not copied into source distributions or
installation packages.

## Source and binary provenance

- `third_party/tcpdump/sources.json` pins official upstream URLs, versions, and
  SHA-256 values. The tcpdump, libpcap, and ARM/MIPSEL musl archives are included unchanged.
- MIPSEL uses the fixed OpenWrt 24.10.0 ramips/mt7621 x86_64-host toolchain.
  Its official archive URL/hash, GNU GCC 13.3.0 source URL/hash, and the
  matching OpenWrt toolchain recipes/patches are recorded in `sources.json`.
  The installed libc is built from our unchanged musl archive, rather than
  copied from the OpenWrt sysroot. GCC's runtime is compiled by GCC with its
  applicable Runtime Library Exception; keep `gcc-mipsel-LICENSE` with it.
- `third_party/tcpdump/binaries.json` pins the validated static executable hashes,
  architecture baselines, toolchains, and the libpcap compatibility-patch hash.
- `third_party/tcpdump/patches/libpcap-ethtool-enotty.patch` is applied only in a
  temporary build tree; the upstream archives remain original.
- `third_party/tcpdump/upstream-NOTICES-LICENSE` conservatively aggregates
  copyright/SPDX comment notices from all C-family source files in the pinned
  tcpdump and libpcap archives, plus their exact general license text. It is
  deliberately not limited to the objects used by a particular architecture.
  The archives preserve every original file and notice; the aggregation is not
  intended to replace them.
- The source export retains the prebuilt private executables because the normal
  offline package builder requires them. Rebuilding them is optional; see
  `third_party/tcpdump/README.md` and `tools/build_tcpdump.py`.

## Redistribution

Keep the unchanged source archives, source hashes, patch, license files,
`upstream-NOTICES-LICENSE`, and this document with source distributions. The
existing package builder includes every `*-LICENSE` file and `sources.json`
under `/usr/lib/radarsender/licenses/` in binary installations, including the
new upstream aggregation. Preserve those installed notices in redistributed
packages. Upstream copyright holders and contributors must not be presented as
endorsing RadarSender.

The precompiled executable hashes document the supplied artifacts; reproducible
bit-for-bit output across changing Debian package revisions is not claimed.
