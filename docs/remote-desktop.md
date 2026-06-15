# RDev Remote Desktop Design

This document describes a cross-platform remote desktop feature for RDev. It is intentionally separate from the existing SSH terminal/file/port-forwarding path so the current remote-debug flow stays stable while GUI streaming evolves.

## Current status

- Phase 1 capability discovery is implemented for browser/device APIs.
- Phase 2 view-only MVP is implemented for Linux X11 and Windows Win32/GDI with the default `CGO_ENABLED=0` client build.
- Linux Wayland and macOS still report limited/unavailable capability until non-cgo native capture paths are implemented.
- Input control, clipboard sync, tile diffing, and hardware/WebRTC acceleration remain future phases.

## Goals

- Provide browser-based remote screen viewing and optional keyboard/mouse control.
- Keep the default client build `CGO_ENABLED=0` and portable across Linux, macOS, and Windows.
- Prefer pure Go, WebAssembly, browser APIs, OS protocols, and direct syscalls.
- Avoid external helper processes on the default path; use them only as optional fallback or diagnostics.
- Allow platform-specific acceleration without making it mandatory for the base build.

## Non-goals

- Do not depend on X11/Wayland/macOS/Windows native libraries through cgo in the default client.
- Do not require external tools such as `ffmpeg`, `gstreamer`, `xwd`, `xdotool`, `grim`, `screencapture`, or PowerShell on the default path.
- Do not ship a kernel driver, virtual display driver, or privileged helper as part of the first version.
- Do not replace mature desktop protocols such as RDP, VNC, SPICE, or Sunshine/Moonlight for high-FPS gaming/graphics workloads.

## Proposed architecture

```
┌──────────────────┐       WebSocket/WebRTC       ┌──────────────────┐
│ Browser Viewer   │ ◄──────────────────────────► │ rdev-server      │
│ Canvas + WASM    │                              │ relay/auth/UI    │
└──────────────────┘                              └────────┬─────────┘
                                                           │
                                                           │ existing device tunnel
                                                           │
                                                   ┌───────▼──────────┐
                                                   │ rdev-client       │
                                                   │ capture + input   │
                                                   └──────────────────┘
```

The server should remain a relay and policy point. The device client owns screen capture and input injection because those operations require local desktop permissions.

## Protocol outline

Add a new logical channel family alongside terminal, file, and TCP forwarding:

- `desktop_offer`: server asks a device to start a desktop session.
- `desktop_ready`: device reports supported backends, monitors, pixel format, and permissions.
- `desktop_frame`: device sends encoded frame chunks or delta frames.
- `desktop_cursor`: device sends cursor image/position changes.
- `desktop_input`: browser sends mouse, wheel, keyboard, clipboard, or touch events.
- `desktop_resize`: browser requests viewport scale/quality/FPS changes.
- `desktop_close`: either side closes the desktop session.

Use binary frames for high-volume data. Keep JSON text frames for metadata and control.

## Encoding strategy

Recommended phases:

1. **MVP MJPEG/PNG tiles**: simple, debuggable, works with pure Go image encoders, acceptable for low-FPS support sessions.
2. **WASM codec in browser**: decode optimized delta/tile stream in browser without server CPU cost.
3. **WebCodecs/WebRTC optional path**: use browser hardware decode where available and fall back to the WASM/tile path.

For a non-cgo default build, avoid mandatory Go bindings to FFmpeg, GStreamer, VAAPI, MediaFoundation, VideoToolbox, or NVENC. Also avoid making external encoder processes mandatory. If hardware encoding is desired later, expose it as an optional backend behind capability detection.

## Default dependency policy

The default desktop implementation should follow this priority order:

1. **Pure Go protocol/syscall backend**: direct Win32 syscalls, X11 protocol, D-Bus/PipeWire protocol, or browser-side WASM/WebCodecs.
2. **Pure Go software fallback**: JPEG/PNG/tile-diff encoding with `image/*` packages and no native dependency.
3. **Optional external-process fallback**: allowed only when explicitly enabled or when the user accepts degraded portability.
4. **Optional native/GPU backend**: allowed only behind build tags or runtime capability detection and must not be required by the default binary.

The release target remains: `CGO_ENABLED=0`, no required external process, and graceful capability reporting when a desktop backend is unavailable.

## Platform backend matrix

| Platform | Screen capture default | Input control default | Notes |
| --- | --- | --- | --- |
| Windows | Pure Go Win32/GDI syscall first; DXGI Desktop Duplication later | Pure Go `SendInput` syscall | Best fit for `CGO_ENABLED=0` and no external process. GPU capture/encoding can be added later as optional backend. |
| Linux X11 | Pure Go X11 protocol first; XShm later | Pure Go XTest protocol | Good MVP target. Requires implementing enough X11/XTest protocol in Go. |
| Linux Wayland | Capability detection first; pure Go portal/PipeWire backend later | Usually unavailable without compositor-approved portal or privileged input path | Hard because of Wayland security policy, not because of Go. Start as unsupported/limited unless portal backend exists. |
| macOS | Capability detection first; pure Go CoreGraphics/Objective-C runtime bridge later | Quartz event injection later | Hardest without cgo or external processes. Requires Screen Recording and Accessibility permissions. |

The MVP should probe backend availability at runtime and report capability flags in `desktop_ready` instead of hard failing. The first fully functional targets should be Windows and Linux X11; Wayland and macOS should start with honest capability reporting and limited support until native non-cgo backends are implemented.

## Package layout

Suggested Go packages:

- `internal/desktop`: common session, monitor, frame, cursor, and input types.
- `internal/desktop/capture`: backend interface and platform-specific implementations.
- `internal/desktop/input`: input interface and platform-specific implementations.
- `internal/server/desktop.go`: WebSocket relay between browser and device.
- `web/desktop.html`: browser viewer using Canvas, pointer lock, clipboard gates, and optional WASM decoder.

Build tags should keep platform code isolated:

- `capture_linux.go`
- `capture_darwin.go`
- `capture_windows.go`
- `capture_unsupported.go`
- `input_linux.go`
- `input_darwin.go`
- `input_windows.go`
- `input_unsupported.go`

## Security model

- Require the same device password/admin-token gates as Web Terminal before starting desktop sessions.
- Default to view-only; require an explicit UI confirmation to enable input control.
- Show a visible session indicator in the Web UI.
- Support per-device policy flags: `--desktop`, `--desktop-view-only`, `--desktop-input`.
- Never enable clipboard sync by default; make it opt-in per session.
- Log session start/stop, viewer identity, and input-control transitions.

## Implementation phases

### Phase 1: Capability discovery

- Add protocol messages and client-side backend probing.
- Expose `/api/devices` capability fields in Web UI.
- No screen streaming yet.

### Phase 2: View-only MVP

- Add `desktop.html` viewer.
- Implement Windows screenshot capture with pure Go Win32/GDI syscalls.
- Implement Linux X11 screenshot capture with a pure Go X11 backend.
- Encode PNG/JPEG frames and stream over existing WebSocket tunnel.
- Add adaptive FPS/quality controls.

### Phase 3: Input control

- Add normalized input event schema.
- Implement Windows input injection with pure Go `SendInput` syscalls.
- Implement Linux X11 input injection with pure Go XTest protocol.
- Add permission/error reporting and UI safeguards.
- Keep input disabled by default and require explicit opt-in.

### Phase 4: Performance

- Add tile diffing and dirty-region updates.
- Add WASM decoder for custom frame packing.
- Use browser WebGL/WebGPU where useful for scaling and tile composition.
- Add optional WebRTC/WebCodecs path for low latency when browser support is available.

### Phase 5: Native and GPU acceleration

- Add Windows DXGI Desktop Duplication backend without cgo if practical.
- Research Windows MediaFoundation or hardware encoder access through pure Go syscalls.
- Add Linux XShm/XDamage acceleration for X11.
- Research Wayland portal/PipeWire capture through pure Go D-Bus/PipeWire protocol.
- Research macOS CoreGraphics/ScreenCaptureKit access through a non-cgo runtime bridge.
- Keep all GPU paths optional and behind capability detection.

## Why not pure WASM for capture?

Browser WASM cannot capture or control the remote device desktop by itself. WASM is useful in the browser for decoding frames, rendering, compression helpers, and possibly protocol logic. Actual capture/input must run on the remote device through OS APIs, OS protocols, direct syscalls, desktop portals, or optional native integrations.

## Recommended first patch

Start with Phase 1, then implement a view-only MVP for Windows and Linux X11 using pure Go/syscall backends. Keep Wayland and macOS as capability-detected limited targets until their non-cgo native paths are proven. This preserves the default `CGO_ENABLED=0` and no-required-external-process release model while still leaving room for optimized native/GPU backends later.
