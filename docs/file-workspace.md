# File workspace

The Files page uses an Explorer-style directory tree and independent preview tabs,
with the existing BB theme. Folders load on expansion; double-clicking a folder
makes it the current root. Up and the path field change the root. Refresh also
invalidates expanded directory caches. Large directories reveal rows in batches.

The Details view retains size, modified time, download and cross-pane transfer
actions. The tree also supports dragging files/folders between device panes.
Uploads, downloads and transfer tasks keep using the existing authenticated socket.

On narrow panes, selecting a file opens the preview surface. Explorer returns to
the directory tree without closing tabs. Each tab preserves its renderer state;
hidden audio/video stop loading and playback. Preview responses are keyed by
request and device, so concurrent reads cannot overwrite another file. Closing,
refreshing, switching devices or disconnecting invalidates outstanding requests.
Loaded text remains viewable after disconnection. Tabs are local to the mounted
workspace, not persisted across a full browser reload.

## Preview formats

- Code and text: syntax-highlighted source, line wrapping and copying.
- Markdown, CSV and HTML: rendered preview and source switching.
- Images: the existing image preview and lightbox.
- Video: browser-supported codecs through the existing media player.
- PDF: the browser PDF viewer, with a separate-tab fallback for mobile browsers.
- Audio: MP3, WAV, Ogg/Opus, M4A, AAC and FLAC, subject to browser codec support.

PDF/audio/video use the same expiring, device-authorized media grants and bounded
Range reads. A changed device password invalidates old grants. No client upgrade
is required for devices that support the existing file transfer protocol. Agent
file previews use these same renderers. Office documents and archives do not yet
have dedicated renderers; download them for a local application.

## Shared tree interactions

The standalone Files workspace and Agent Files panel now share the same tree:
folder-first natural sorting, lazy loading, keyboard navigation, loaded-name
filtering, and context menus for copying paths, download, rename and delete.
Folder menus offer new files/folders. Expanded folders and selection are retained
per device/workspace/root in session storage. New-file creation uses an exclusive create operation, so concurrent creators cannot
overwrite an existing file. Rename uses native no-replace semantics on Go, Rust
and Android clients. File-system case sensitivity is respected.

Desktop tree/preview dividers support pointer and keyboard resizing and retain
their split per workspace. Narrow standalone panes show either tree or preview;
the Agent panel uses a vertical split, switching to one surface on mobile.
Directory refresh invalidates child caches while retaining expanded paths. Lazy
listing requests are isolated from navigation and preview requests and canceled on
disconnection. Each lazy directory is bounded to 10,000 entries; larger directories
can be opened directly and paged using Load more.

Rename/delete require the updated Go or Rust client; Android already supports the
protocol, and now rejects root mutation and existing rename destinations and does
not follow final symlinks during deletion. Update these clients with the server/UI.
Registration advertises supported file operations; unsupported actions are disabled
and the server rejects them immediately. Outstanding requests have a 25-second
deadline, bounded counts and device/disconnection isolation. Recursive deletion
requires explicit confirmation and does not use a recycle bin.

Directory pagination reuses short-lived snapshots (10 seconds, eight directories,
32 MiB total per client). First-page requests refresh the snapshot; local mutations
invalidate it. Rust and Android uploads reject concurrent writers to one path.

## Direct transfers and bounded disk downloads

With server `--webrtc` (or `RDEV_WEBRTC=true`), updated Go and Rust clients
can transfer file bytes over a reliable ordered WebRTC DataChannel. Authorization
and signaling stay on the existing WebSocket. Devices without RTC support use
relay, but must support the integrity contract below. Direct uploads pipeline at most 512 KiB (32 chunks)
and resume through relay from the last confirmed offset after a direct failure.
Sends remain ordered while waiting for DataChannel backpressure, including a short
final chunk that would otherwise fit ahead of a blocked full chunk.
The DataChannel send-buffer high-water remains 512 KiB. A delayed-ACK simulation
using real Blob reads (4 MiB + 13 bytes, median of three runs) measured 4.434 MiB/s
at 100 ms with the 512 KiB window versus 2.348 MiB/s with 256 KiB. This is not
a real-network throughput guarantee.
Task diagnostics show negotiation/direct/relay state, fallback reason, sampled RTT
and buffered bytes; they do not display SDP, addresses or ICE credentials.

Configure ICE using `--webrtc-ice-servers` or `RDEV_WEBRTC_ICE_SERVERS`, for example
`[{"urls":["turn:relay.example:3478?transport=udp"],"username":"user","credential":"secret"}]`.
Omission retains the public STUN default; explicit `[]` selects host-only ICE.
The authenticated, non-cacheable API and authorized device offers receive the same
configuration. Keep TURN credentials in protected configuration, not command logs.
This setting applies to desktop and file RTC; it does not provision a TURN server.

On browsers with File System Access, Download opens the save picker during the
user gesture. Writes are serialized and bounded to a 4 MiB requested range; the
next range starts only after disk commits. Completion waits for the file to close.
Updated peers negotiate `rangeAck: true` in the RTC offer and answer for bounded
downloads. One peer and open source file then serve all sequential ranges. Each
intermediate `0x23` end waits for a 10-byte `0x25` frame (empty identity/payload,
big-endian committed offset exactly matching that end) before the next requested
range. No path or seek is accepted through this acknowledgement. Duplicate,
premature or mismatched acknowledgements close the peer. Disk-commit waits have a
30-second watchdog. Only the final range emits the WebSocket terminal message;
the browser closes RTC after receiving and draining the final range. Peers without
the negotiated flag retain per-range connections; relay remains bounded as before.
Waiting in the native save picker or opening its writable destination does not
trigger automatic download retries or another picker. Canceling an active download
aborts uncommitted writes; it is not reported as a saved partial download. Once
the final writer close begins, the task enters **saving** and pause, resume,
cancel and clear are disabled until close succeeds or fails. This irreversible
commit cannot be represented as a successful cancellation.
Pause/fallback resumes from committed bytes, not merely received bytes. A browser
reload cannot restore the destination handle: select a destination again and
restart. Before saving begins, cancel aborts the writable destination. Devices must
honor bounded range requests; an overrun fails explicitly rather than buffering an
unbounded file.
Browsers without the save API enforce a visible **64 MiB per-download memory
limit** and a shared **two retained download** admission limit across all Files
panes and Agent panels. Paused downloads retain a slot; a third download fails
with instructions to finish/cancel another task and retry or use disk-save support.
Failed/canceled tasks discard their buffered bytes. Saved downloads retain their
slot until the object URL is revoked; unmount releases buffers and URLs immediately.
Metadata and actual bytes are both checked, so dishonest sizes cannot exceed the
per-file bound. Disk downloads and bounded cross-device streaming do not consume
these fallback slots.

## Transfer integrity and cross-device copy

Updated clients are required for transfers; there is no unverified legacy upload
bypass. Go, Rust and Android reject upload starts without a valid lowercase
full-source `sha256` and exact `size`. An existing `.rdevpart` without matching
SHA-256 and size identity metadata is reset to offset zero for a valid new upload.
The browser computes local file hashes incrementally with 256 KiB reads and yields between reads; fingerprints
are cached only for the same live source object. Device `upload_ready` must echo
`sha256` and include `prefixSha256` for the exact persisted offset. The browser
verifies that prefix before sending any bytes, including after RTC-to-relay
fallback. A mismatching prefix fails visibly; Retry sends `restart: true` to reset
the corrupt partial and requires a verified zero offset. Reselecting a same-name,
same-size but different file never silently reuses old bytes.

Download metadata includes `sha256`; every resumed request echoes it. A changed
source is rejected. Disk and Blob destinations verify the entire received hash
before saving, and completion joins the binary end with the authoritative
`transfer_end` (`success: true`, matching `sha256`). Disk verification happens
before close, preserving the prior target on failure. Stale attempt completions
cannot commit a canceled or replaced transfer.

Cross-pane files no longer download into whole-file Blob/File objects. The source
pane exposes an identity-bound range reader to a normal destination upload task.
Each source range is at most 512 KiB; the destination's acknowledged upload window
controls subsequent reads. Prefix verification reads the source in bounded chunks.
Source mutation, range overrun, timeout or disconnect fails the destination task;
pause/cancel aborts the pending range and late replies are discarded. Retry opens
fresh range requests with the original fingerprint. Directory walking awaits
source setup, preserves the captured endpoints, and creates empty directories.
The source pane must remain mounted during a cross-pane transfer.
