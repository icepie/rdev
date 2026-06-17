# rdev-client-gpu

Experimental Rust client for the future GPU-friendly RDev desktop path.

Current milestone:

- Connects to an RDev server over `/ws`.
- Registers with `clientId`, `instanceId`, optional password, `clientVersion` (`rs/<version>`), and staged GPU desktop capabilities.
- Supports SSH shell/exec session transport with stdout/stderr binary frames.
- Supports PTY sessions via `portable-pty`.
- Supports `sftp` subsystem via embedded SFTP v3 handling built on `russh-sftp`.
- Supports TCP forwarding protocol (`tcp_connect` and device-side listeners for reverse forwarding).
- Supports Web file manager list/upload/download with offset binary frames.
- Supports batch file distribution binary frames (`file_put`/streamed file writes).
- Supports an RDev/Weylus-style GPU desktop tunnel: `/gpu-desktop-tunnel` proxies browser HTTP/WebSocket streams to the embedded RDev desktop service when built with `embedded-rdev-desktop`.

The Go `rdev-client` remains the default portable client. Release builds of this Rust client are the optional performance flavor and include the embedded desktop path by default.

## Build

From the repository root:

```bash
make rust-client-gpu
# or
cargo build --release --manifest-path clients/rdev-client-gpu/Cargo.toml
```

The regular Cargo build uses current crate releases within the `rust-version` declared in `Cargo.toml`. The release/package targets enable embedded desktop support for the performance client.

To build and stage the Linux release package, run:

```bash
make rust-client-gpu-linux-desktop-package
```

The package script installs the native build dependencies for the detected distro, builds vendored FFmpeg/x264/libva from source, and stages the result in `clients/rdev-client-gpu/dist/`. CI reuses one runner per Linux architecture and builds multiple distro-baseline packages in containers: the default `rdev-client-gpu-linux-<arch>.tar.gz` is Debian 13 with VAAPI enabled, while `*-debian12`, `*-ubuntu2004`, `*-centos8`, and amd64-only `*-centos7` are compatibility packages without VAAPI for older glibc/driver stacks. NVENC remains opt-in with `RDEV_GPU_FEATURES=embedded-rdev-desktop-hw ENABLE_NVENC=y`.

For a direct embedded desktop build without staging:

```bash
cargo build --release --manifest-path clients/rdev-client-gpu/Cargo.toml --features embedded-rdev-desktop
```

The Windows packages are cross-compiled from Linux with `llvm-mingw`. Windows amd64 and Windows 7 amd64 use `x86_64-pc-windows-gnullvm`; Windows ARM64 uses `aarch64-pc-windows-gnullvm`. The Windows 7 package then applies PE import patches and ships compatibility shim DLLs for Win8+ imports such as `GetSystemTimePreciseAsFileTime`, `WaitOnAddress`, and `ProcessPrng`.

For a Windows ARM64 artifact from a Linux host, install an LLVM MinGW toolchain that provides `aarch64-w64-mingw32-clang`, then run:

```bash
rustup target add aarch64-pc-windows-gnullvm
make rust-client-gpu-windows-arm64-package
```

For a Windows 7-compatible amd64 artifact, install an LLVM MinGW toolchain that provides `x86_64-w64-mingw32-clang`, then run:

```bash
rustup target add x86_64-pc-windows-gnullvm
make rust-client-gpu-win7-package
```

This uses the normal `x86_64-pc-windows-gnullvm` release build, patches known Win8+ PE import names, builds compatibility shim DLLs, auto-copies WinPTY runtime files when they can be found, and places everything in `clients/rdev-client-gpu/target/win7-dist/`.

## Run

From the repository root, after `make rust-client-gpu`:

```bash
./clients/rdev-client-gpu/target/release/rdev-client-gpu \
  --server wss://r.feidu.fit \
  --id my-device \
  --password ''
```

Useful flags:

- `--shell /bin/bash` selects the shell used for exec/shell sessions.
- `--instance-id <id>` pins the reconnect identity for tests.
- `--no-desktop` registers without staged desktop capabilities and disables the GPU desktop tunnel.
- `--gpu-desktop-local 127.0.0.1:1701` selects where the embedded RDev-compatible desktop service listens.
- `--no-gpu-desktop-tunnel` disables only the GPU desktop tunnel while keeping other client features.
- `--reconnect-delay 2s` controls reconnect backoff.

## Validation

```bash
make rust-client-gpu-check
make rust-client-gpu-smoke
```

The smoke test starts a local Go `rdev-server`, connects this Rust client, then validates SSH exec, PTY, SCP, rsync-over-SSH, embedded SFTP, Web file manager list/upload/download, and local TCP forwarding. It requires `ssh`, `scp`, `sftp`, `sshpass`, `rsync`, `curl`, `go`, `cargo`, and `python3`.

Windows 7 notes:

- Use the normal `x86_64-pc-windows-gnullvm` release build, then package with `make rust-client-gpu-win7-package`.
- Deploy all files from `target/win7-dist` into the same directory: `rdev-client-gpu.exe`, `rdev-waitonaddress-shim.dll`, `rdev-bcprng.dll`, `rdevws.dll`, and optional `winpty.dll`/`winpty-agent.exe`.
- Packaging auto-detects WinPTY from `RDEV_WINPTY_DIR`, `WINPTY_DIR`, common local Node/Git-Bash locations, and `PATH`; missing WinPTY only warns because pipe fallback remains available.
- Runtime PTY order is Win7/Win8: WinPTY then pipe fallback; Win10/Win11: `portable-pty`/ConPTY then pipe fallback.
- Use `make rust-client-gpu-win7-smoke` for real Win7 E2E validation; set `RDEV_GPU_WIN7_HOST`, `RDEV_GPU_WIN7_PASSWORD`, and optional `RDEV_GPU_WIN7_PORT`/`RDEV_GPU_WIN7_USER` first.
- Use `ws://` for direct client-server tests. `wss://` is configured to use Windows Schannel instead of Rustls on Windows, but older Win7 TLS root/cipher support can still vary by host patch level.

## GPU desktop direction

The server does not decode desktop video. It opens `/gpu-desktop/<device>/` for browsers and multiplexes raw HTTP/WebSocket streams over `/gpu-desktop-tunnel` to the Rust client. With `embedded-rdev-desktop`, the Rust client starts vendored RDev/Weylus web, capture, input, and H.264 encoder code in-process and tunnels browser traffic to that local service. Builds without the feature do not register the GPU desktop tunnel.

## Release packages

The `Rust Client GPU` GitHub Actions workflow publishes artifacts on normal pushes and uploads them to GitHub Releases on `v*` tags.

- Public `rdev-client-gpu-*` packages are the performance flavor and include embedded desktop support by default, including Windows amd64, Windows ARM64, and Windows 7 amd64.
- Linux packages use VAAPI+x264 by default and can opt into NVENC for dedicated NVIDIA builds.
- Linux release packages are built in distro environments to avoid accidentally requiring the newest runner glibc.

## Next

1. Harden Rust client validation across Windows and macOS hosts.
2. Add signed/notarized macOS packaging when distribution requires it.
3. Add signed/notarized Windows/macOS installers when distribution requires them.
