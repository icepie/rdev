# RDev framing (normative)

This document freezes the bytes on the wire for the two framing layers RDev
ships today, and the third layer used by file transfers. It is derived from the
implementations, not from intent:

| Layer | Go | Rust | Java | Web |
| --- | --- | --- | --- | --- |
| A. stream frame | `internal/transport/framing.go` | `clients/rdev-client-rs/src/stream_frame.rs` | `RDevStreamFrame.java` | — |
| B. binary frame | `internal/protocol/protocol.go` (`EncodeBinFrame`, `DecodeBinFrame`) | `clients/rdev-client-rs/src/protocol.rs` (`encode_bin_frame`, `decode_bin_frame`) | `RDevProtocol.java` (`encode`, `decode`) | — |
| C. file-transfer frame | B with an offset word (`EncodeBinFrameOffset`) | `encode_bin_frame_offset` / `decode_bin_frame_offset` | `encodeOffset` / `decode` (offset types) | `web/ui/lib/file-transfer-frame.ts` |

Changing any byte here is a protocol break: add a fixture row, update all three
tests, and keep old devices working. Never change the format to make a decoder
simpler.

## Numeric conventions

- All integers are network order (big-endian).
- `u8`, `u16`, `u32`, `u64` mean unsigned widths; offsets are stored as `u64`
  bit patterns and interpreted as signed `int64` on the wire consumer side, so a
  negative offset (e.g. `ffffffffffffffff`) round-trips unchanged.
- "ID length" is always the **UTF-8 byte** length, never the character count.
  `任务-1` is 8 bytes (`e4bbbbe58aa12d31`).

## Layer A: stream frame

Used on every reliable byte stream between a device and the server: raw TCP,
KCP (`udp://`/`kcp://` endpoints), and the GPU desktop tunnel. Both transports
deliver the same bytes because framing is applied above the stream, so a KCP
peer is byte-identical to a TCP peer.

```
offset  size  field
0       1     kind
1       4     payload length, u32 BE (counts payload bytes only)
5       N     payload
```

| kind | name | payload |
| --- | --- | --- |
| 1 | JSON | one JSON control message (UTF-8) |
| 2 | BINARY | one layer-B frame (see below) |
| 3 | PING | liveness token; opaque to the receiver |
| 4 | PONG | echo of the PING payload |
| 5 | CLOSE | close reason text |

Rules:

- Total frame size is `5 + length`. There is no padding, terminator, or
  checksum; the next frame starts immediately at `5 + length`.
- `length` may be 0 (PING and empty payloads are legal). The receiver must not
  read payload bytes when `length == 0`.
- `length > 16777216` (16 MiB, `MaxFramePayload`/`MAX_FRAME`) is rejected
  **before** allocating, with a "too large" error.
- Fewer than 5 header bytes, or fewer payload bytes than `length`, is a
  truncated frame: EOF/unexpected-EOF. No partial frame is ever surfaced.
- A stream may end between frames; that is a clean EOF, distinct from a
  truncated frame.

Example, `{"type":"register"}` as JSON:

```
01 00000012 7b2274797065223a227265676973746572227d
```

The WebSocket device transport does **not** wrap frames in layer A: a WS binary
message carries one layer-B frame verbatim, and a WS text message carries the
JSON payload directly. Layer A exists only for raw streams.

## Layer B: binary frame

Carried inside a layer-A BINARY frame, or as one WS binary message.

```
offset  size  field
0       1     type
1       1     id length, u8 (0..255)
2       L     id, UTF-8, exactly L bytes
2+L     R     body (layout depends on type)
```

Global rules:

- `L` is a byte count. `L = 0` is legal (see the ID rules below).
- The frame has no own length field. Its boundary is the container: the layer-A
  `length`, or the WS message length. Trailing bytes belong to the body.
- `len(raw) < 2` or `len(raw) < 2 + L` is invalid; decoders reject it instead of
  clamping.
- An ID longer than 255 bytes cannot be expressed; encoders reject it
  (`id too long` in Go/Rust) — never truncate silently.

Body layouts by type:

| type | name | body |
| --- | --- | --- |
| 0x01 | BIN_DATA | terminal stdin/stdout bytes |
| 0x02 | BIN_STDERR | terminal stderr bytes |
| 0x03 | BIN_TCP_DATA | forwarded TCP bytes |
| 0x04 | BIN_FILE_PUT | `u16 pathLen` + path + `u32 mode` + file data |
| 0x05 | BIN_FILE_START | `u16 pathLen` + path + `u32 mode` |
| 0x06 | BIN_FILE_CHUNK | file data |
| 0x07 | BIN_FILE_END | file data tail |
| 0x08 | BIN_FILE_ACK | acknowledgement |
| 0x20 | BIN_FILE_UPLOAD_CHUNK | layer C (offset + bytes) |
| 0x21 | BIN_FILE_UPLOAD_ACK | layer C |
| 0x22 | BIN_FILE_DOWNLOAD_CHUNK | layer C |
| 0x23 | BIN_FILE_TRANSFER_END | layer C |
| 0x24 | BIN_FILE_TRANSFER_CANCEL | layer C |
| 0x30 | BIN_DESKTOP_FRAME | encoded desktop frame |

Only `0x20`–`0x24` carry an offset. For every other type the body is opaque
payload and starts right after the ID — a body that happens to be 8 bytes or
longer is still payload, not an offset word.

## Layer C: file-transfer frame

Layer B restricted to `0x20`–`0x24`, with an 8-byte offset prefixed to the body:

```
offset  size  field
0       1     type (0x20..0x24)
1       1     id length, u8
2       L     id, UTF-8
2+L     8     offset, u64 BE (interpreted as int64)
10+L    N     payload bytes
```

Rules:

- `len(raw) < 10 + L` is a truncated offset frame. Decoders reject it; they must
  not fabricate `offset = 0`.
- Payload length is `len(raw) - 10 - L`; it may be 0 (ACK/END/CANCEL).
- Offsets are absolute byte positions in the transferred file, monotonic per
  direction but not required to be contiguous. `0x22` download chunks start at
  the resume offset; `0x20` upload chunks start at the resume offset.
- `offset` must not exceed `2^53 - 1` on the Web path, because JS must represent
  it exactly; Web decoders reject larger values. Go/Rust/Java use full `int64`.
- The Web decoders additionally require a non-empty ID (see below).

Example, download chunk `task-512k` at offset 524288 (`0000000000080000`) with
16 bytes of payload: `22 09 7461736b2d3531326b 0000000000080000 ffff…`.

## ID rules per transport

The ID always names the peer-side endpoint: a terminal session, a forward, a
file task. Which values are legal depends on the path:

| path | encoded by | ID |
| --- | --- | --- |
| Device link over TCP/KCP/WS, session and forward data | device + server | non-empty; matches a live session/forward |
| Device link, file transfer (`0x20`–`0x24`) | server, Android device | non-empty task id (Android always writes the task id) |
| Device link, direct file transfer (Rust client `file_rtc`/`files`) | `rdev-client-rs` | **may be empty** — the transfer is bound by the RTC session/task already; peers accept `id = ""` |
| WebSocket relay (browser ⇄ server, `/files`) | `web/ui/lib/file-transfer-frame.ts` | **required, non-empty**; `encodeFrame` throws and `decodeFrame` returns `null` |
| WebRTC data channel (browser ⇄ device) | `encodeFileRTCFrame` | **always empty** (`idLen = 0`); the peer supplies the id and `remapFileRTCFrame` re-wraps it as a relay frame with the task id |

That asymmetry is intentional and must be preserved:

- Go `protocol.DecodeBinFrameOffset` accepts an empty ID; `internal/server/files.go`
  and `internal/server/agent_tools.go` route device frames by task.
- Rust `decode_bin_frame_offset` accepts an empty ID; `files.rs`, `file_rtc.rs`
  and `fileput.rs` accept `""` or the owned task id and reject foreign ids.
- Java `RDevProtocol.decode` accepts an empty ID (it is a generic device
  decoder), but the Android sender always writes the task id for file frames.
- Web `decodeFrame` rejects `idLen = 0`; the WS relay never emits or accepts an
  anonymous file frame. WebRTC frames carry no id at all and are re-wrapped.

## Fixtures

The shared byte corpus lives in `internal/transport/testdata/framing/`:

- `fixtures.tsv` — one row per case, tab-separated, with the expected decode
  result. Sections `[stream]` (layer A) and `[inner]` (layers B + C).
  - stream row: `name, file, expect, kind, payloadHex, remainingHex`; rows
    sharing a file are decoded in order and `remainingHex` is what is left after
    the last row of that file (offset/boundary check).
  - inner row: `name, file, expect, type, id, offseted, offset, payloadHex`;
    `offseted=1` means layer C and `payloadHex` is the bytes after the offset.
  - `expect` is `ok`, `error:eof` (stream truncated before a full frame),
    `error:too_large` (layer-A length over 16 MiB) or `error:invalid` (layer-B/C
    structure rejected: `len < 2`, id truncated, offset truncated).
- `stream/*.bin`, `bin/*.bin` — the raw bytes. No framing is added by the test.

Coverage: network byte order, empty payload, UTF-8 IDs (multi-byte), empty ID,
255-byte ID, offset 0, offset 512 KiB, offset `2^53 - 1`, back-to-back frames
with a trailing partial header, truncated header, truncated id, truncated
offset, 16 MiB + 1 length.

Every implementation asserts against these same bytes:

- Go: `internal/transport/framing_fixture_test.go` (`go test ./internal/transport/ -count=1`)
  decodes layer A with `transport.ReadFrame` and layers B/C with
  `internal/protocol` (the reference Go decoder).
- Rust: `clients/rdev-client-rs/tests/framing_fixtures.rs`
  (`cargo test --manifest-path clients/rdev-client-rs/Cargo.toml`) resolves the
  corpus through `CARGO_MANIFEST_DIR/../../internal/transport/testdata/framing`.
- Java: `clients/rdev-android/app/src/test/java/dev/icepie/rdev/FramingFixtures.java`
  (run by the Android unit tests, or stand-alone with `javac` + `java` when no
  Android SDK is installed) resolves it from `RDEV_FIXTURE_ROOT` or by walking up
  from the working directory.

A case is not covered until all three read the same file. Do not fork the corpus
per language.
