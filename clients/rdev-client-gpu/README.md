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
- Supports an AuroraOps/Weylus-style GPU desktop tunnel: `/gpu-desktop-tunnel` proxies browser HTTP/WebSocket streams to the embedded AuroraOps desktop service when built with `embedded-gpu-desktop`.

The Go `rdev-client` remains the default portable client. This Rust client is intended to coexist with it and can take on heavier platform/GPU dependencies over time.

## Build

From the repository root:

```bash
make rust-client-gpu
# or
cargo build --release --manifest-path clients/rdev-client-gpu/Cargo.toml
```

The regular Rust client uses current crate releases within the `rust-version` declared in `Cargo.toml`. The default build stays lightweight and does not include AuroraOps encoder/capture dependencies. To build the embedded GPU desktop service, install system FFmpeg/X11/DRM development libraries and run:

```bash
cargo build --release --manifest-path clients/rdev-client-gpu/Cargo.toml --features embedded-gpu-desktop
```

The Windows 7 package keeps the normal Windows GNU build, then applies PE import patches and ships compatibility shim DLLs for Win8+ imports such as `GetSystemTimePreciseAsFileTime`, `WaitOnAddress`, and `ProcessPrng`.

For a Windows 7-compatible amd64 artifact:

```bash
rustup target add x86_64-pc-windows-gnu
make rust-client-gpu-win7-package
```

This uses the normal `x86_64-pc-windows-gnu` release build, patches known Win8+ PE import names, builds compatibility shim DLLs, auto-copies WinPTY runtime files when they can be found, and places everything in `clients/rdev-client-gpu/target/win7-dist/`.

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
- `--gpu-desktop-local 127.0.0.1:1701` selects where the embedded AuroraOps-compatible desktop service listens.
- `--no-gpu-desktop-tunnel` disables only the GPU desktop tunnel while keeping other client features.
- `--reconnect-delay 2s` controls reconnect backoff.

## Validation

```bash
make rust-client-gpu-check
make rust-client-gpu-smoke
```

The smoke test starts a local Go `rdev-server`, connects this Rust client, then validates SSH exec, PTY, SCP, rsync-over-SSH, embedded SFTP, Web file manager list/upload/download, and local TCP forwarding. It requires `ssh`, `scp`, `sftp`, `sshpass`, `rsync`, `curl`, `go`, `cargo`, and `python3`.

Windows 7 notes:

- Use the normal `x86_64-pc-windows-gnu` release build, then package with `make rust-client-gpu-win7-package`.
- Deploy all files from `target/win7-dist` into the same directory: `rdev-client-gpu.exe`, `rdev-waitonaddress-shim.dll`, `rdev-bcprng.dll`, `rdevws.dll`, and optional `winpty.dll`/`winpty-agent.exe`.
- Packaging auto-detects WinPTY from `RDEV_WINPTY_DIR`, `WINPTY_DIR`, common local Node/Git-Bash locations, and `PATH`; missing WinPTY only warns because pipe fallback remains available.
- Runtime PTY order is Win7/Win8: WinPTY then pipe fallback; Win10/Win11: `portable-pty`/ConPTY then pipe fallback.
- Use `make rust-client-gpu-win7-smoke` for real Win7 E2E validation; set `RDEV_GPU_WIN7_HOST`, `RDEV_GPU_WIN7_PASSWORD`, and optional `RDEV_GPU_WIN7_PORT`/`RDEV_GPU_WIN7_USER` first.
- Use `ws://` for direct client-server tests. `wss://` is configured to use Windows Schannel instead of Rustls on Windows, but older Win7 TLS root/cipher support can still vary by host patch level.

## GPU desktop direction

The server does not decode desktop video. It opens `/gpu-desktop/<device>/` for browsers and multiplexes raw HTTP/WebSocket streams over `/gpu-desktop-tunnel` to the Rust client. With `embedded-gpu-desktop`, the Rust client starts vendored AuroraOps/Weylus web, capture, input, and H.264 encoder code in-process and tunnels browser traffic to that local service. Builds without the feature do not register the GPU desktop tunnel.

## Next

1. Add CI/package jobs for `embedded-gpu-desktop` on supported Linux runners.
2. Harden Rust client validation across Windows and macOS hosts.
3. Package GPU desktop runtime library dependencies per platform.
