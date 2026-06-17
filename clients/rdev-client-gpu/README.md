# rdev-client-gpu

Experimental Rust client for the future GPU-friendly RDev desktop path.

Current milestone:

- Connects to an RDev server over `/ws`.
- Registers with `clientId`, `instanceId`, optional password, and staged GPU desktop capabilities.
- Supports SSH shell/exec session transport with stdout/stderr binary frames.
- Supports PTY sessions via `portable-pty`.
- Supports `sftp` subsystem by launching the system `sftp-server` when available.
- Supports TCP forwarding protocol (`tcp_connect` and device-side listeners for reverse forwarding).
- Supports Web file manager list/upload/download with offset binary frames.
- Supports batch file distribution binary frames (`file_put`/streamed file writes).
- Keeps the desktop video pipeline disabled until the H.264/WebCodecs work lands.

The Go `rdev-client` remains the default portable client. This Rust client is intended to coexist with it and can take on heavier platform/GPU dependencies over time.

## Build

From the repository root:

```bash
make rust-client-gpu
# or
cargo build --release --manifest-path clients/rdev-client-gpu/Cargo.toml
```

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
- `--no-desktop` registers without staged desktop capabilities.
- `--reconnect-delay 2s` controls reconnect backoff.

## Validation

```bash
make rust-client-gpu-check
make rust-client-gpu-smoke
```

The smoke test starts a local Go `rdev-server`, connects this Rust client, then validates SSH exec, PTY, SCP, rsync-over-SSH, SFTP when `sftp-server` is available, Web file manager list/upload/download, and local TCP forwarding. It requires `ssh`, `scp`, `sshpass`, `rsync`, `curl`, `go`, `cargo`, and `python3`.

## Next

1. Harden Rust client validation across Windows and macOS hosts.
2. Import the AuroraOps capture/encoder pipeline behind optional features.
3. Add H.264 video frame protocol and browser WebCodecs playback.
