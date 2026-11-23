# Android / Termux Client

RDev supports Android as a Termux-hosted client before a native APK is introduced.

## One-click start

In Termux:

```sh
pkg install -y curl
curl -sL https://r.niceaigc.cn/run.sh | sh -s -- wss://r.niceaigc.cn -p YOUR_PASSWORD
```

Use the Rust performance client without desktop capture:

```sh
pkg install -y curl tar
curl -sL https://r.niceaigc.cn/run.sh | sh -s -- wss://r.niceaigc.cn -p YOUR_PASSWORD --client rs
```

## Behavior

- Termux is detected as `android`, not generic Linux.
- The runner downloads Android/Bionic assets such as `rdev-client-android-arm64` or `rdev-client-rs-android-arm64.tar.gz`. Downloads try the RDev server proxy first, then GitHub mirrors, then GitHub direct.
- Go Android assets are built with the Android NDK and cgo so DNS uses Android libc instead of Linux `/etc/resolv.conf` assumptions.
- Android Go builds require `-ldflags "-checklinkname=0"` with Go 1.23+: Pion's `github.com/wlynxg/anet` dependency accesses `net.zoneCache` to support Android network interfaces. This linker opt-in is limited to Android builds; see [anet's build requirements](https://github.com/wlynxg/anet#how-to-build-with-go-1230-or-later).
- The default shell is `$PREFIX/bin/bash`, then `$PREFIX/bin/sh`, then `/system/bin/sh`. The Go and Rust clients use the same order when `SHELL` is unset (for example under Termux:Boot or `nohup`), and `run.sh` recognises Termux from `PREFIX` even when `uname -o` is unavailable. A shell that exists but is not executable is skipped: the clients retry the remaining candidates on `EACCES`/`ENOENT`/`ENOEXEC` and log which shell they used.
- Android artifacts ship as ordinary PIE executables with `PT_INTERP=/system/bin/linker64`; they are **not** UPX-compressed. UPX rewrites the ELF into a self-decompressing stub without an interpreter, which Android 10+ refuses to execute. CI fails the build if an Android asset loses its interpreter. Expect roughly 18 MB for the Go client and a larger `rdev-client-rs` archive as a result.
- The Android Rust package is a no-desktop terminal/file/session client. Screen capture and input need a future APK because Android requires `MediaProjection` and `AccessibilityService` user grants.

## Troubleshooting

- `Permission denied` or `cannot execute: required file not found` for a downloaded client means the binary lost its interpreter (an old UPX build) or was written to a `noexec` path. Upgrade to a release after the uncompressed-asset change, or set `TMPDIR` to an executable directory.
- DNS failures mean the client was built without cgo. Use the published `android/*` assets rather than a local `CGO_ENABLED=0` build.

## Limitations

- Termux cannot provide Android screen capture or touch injection by itself.
- Ordinary Termux permissions are limited to the Termux app sandbox plus Android storage grants.
- Root, ADB, or Device Owner mode can provide broader device control, but is intentionally separate from the default client path.
