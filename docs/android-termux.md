# Android / Termux Client

RDev supports Android as a Termux-hosted client before a native APK is introduced.

## One-click start

In Termux:

```sh
pkg install -y curl
curl -sL https://rdev.singzer.cn/run.sh | sh -s -- wss://rdev.singzer.cn -p YOUR_PASSWORD
```

Use the Rust performance client without desktop capture:

```sh
pkg install -y curl tar
curl -sL https://rdev.singzer.cn/run.sh | sh -s -- wss://rdev.singzer.cn -p YOUR_PASSWORD --client rs
```

## Behavior

- Termux is detected as `android`, not generic Linux.
- The runner downloads Android/Bionic assets such as `rdev-client-android-arm64` or `rdev-client-gpu-android-arm64.tar.gz`.
- Go Android assets are built with the Android NDK and cgo so DNS uses Android libc instead of Linux `/etc/resolv.conf` assumptions.
- The default shell is `$PREFIX/bin/bash`, then `$PREFIX/bin/sh`, then `/system/bin/sh`.
- The Android Rust package is a no-desktop terminal/file/session client. Screen capture and input need a future APK because Android requires `MediaProjection` and `AccessibilityService` user grants.

## Limitations

- Termux cannot provide Android screen capture or touch injection by itself.
- Ordinary Termux permissions are limited to the Termux app sandbox plus Android storage grants.
- Root, ADB, or Device Owner mode can provide broader device control, but is intentionally separate from the default client path.
