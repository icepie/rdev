# RDev Remote Desktop Design

This document describes a cross-platform remote desktop feature for RDev. It is intentionally separate from the existing SSH terminal/file/port-forwarding path so the current remote-debug flow stays stable while GUI streaming evolves.

## Current status

- Phase 1 capability discovery is implemented for browser/device APIs.
- Phase 2 screen streaming is implemented for Linux X11, Linux framebuffer (`fbdev` root/KMS-console fallback), Linux DRM/KMS linear-scanout fallback, and Windows Win32/GDI with the default `CGO_ENABLED=0` client build.
- Capture source selection now supports Auto, all screens, individual monitors, and visible windows on Windows GDI and Linux X11; Linux framebuffer exposes framebuffer screen sources; Linux DRM/KMS exposes active scanout screen/connector sources when available. Legacy `primary`/`virtual` source IDs are accepted only for compatibility and are not shown in the UI.
- Phase 3 input control is implemented for Windows Win32 and Linux X11. Linux framebuffer, Wayland-unimplemented, and macOS-unimplemented paths are view-only until native input/capture backends are added.
- The default stream is JPEG frames over binary WebSocket frames, rendered on a browser Canvas; this is effectively an MJPEG-style transport over the existing relay.
- Browser auto mode requests an adaptive resolution based on viewport size and device pixel ratio; manual mode keeps explicit FPS/quality/max-size controls.
- Capability discovery is intentionally non-invasive: clients enumerate metadata and sources with lightweight OS APIs/ioctls, but they do not start capture, export PRIME buffers, mmap framebuffer memory, or inject input during registration. Expensive or stateful operations happen only after an authenticated `desktop_start` selects a source.
- Linux Wayland reports the native `wayland-portal` backend as planned and never shells out to `grim`/`slurp`; root/no-display sessions can fall back to `drm-kms` when active linear scanout metadata is visible, otherwise to `fbdev` when a readable, unblanked framebuffer is present.
- macOS reports Quartz/CoreGraphics capability metadata in the default no-cgo build, but capture remains planned until a native backend is implemented without making cgo mandatory.
- Clipboard sync, tile diffing, and hardware/WebRTC acceleration remain future phases.

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

- `desktop_start`: server asks a device to start a desktop session with source/FPS/quality/max-size settings. Source IDs are structured as `screen:all`, `monitor:<id>`, `window:<id>`, or `fbdev:<path>`; `auto` chooses the best default.
- `desktop_ready`: device reports supported backends, sources, pixel format, dimensions, and permissions.
- `desktop_frame`: device sends encoded JPEG frame bytes as `BinDesktopFrame` binary WebSocket frames.
- `desktop_input`: browser sends normalized mouse, wheel, and keyboard events through the server to the device.
- `desktop_close`: either side closes the desktop session.

Use binary frames for high-volume data. Keep JSON text frames for metadata and control.

## Encoding strategy

Recommended phases:

1. **MVP MJPEG/JPEG stream**: simple, debuggable, works with pure Go image encoders, acceptable for low-FPS support sessions. This is the current default.
2. **Tile diffing / dirty regions**: keep JPEG/PNG compatibility while avoiding full-frame uploads when only small regions change.
3. **WASM codec in browser**: decode optimized delta/tile stream in browser without server CPU cost.
4. **WebCodecs/WebRTC optional path**: use browser hardware decode where available and fall back to the WASM/tile path.

For a non-cgo default build, avoid mandatory Go bindings to FFmpeg, GStreamer, VAAPI, MediaFoundation, VideoToolbox, or NVENC. Also avoid making external encoder processes mandatory. If hardware encoding is desired later, expose it as an optional backend behind capability detection.

## Implemented capture backends

- **Windows Win32/GDI**: pure Go syscall backend, compatible with Windows 7 and newer. It supports all-screen, monitor, and visible-window capture.
- **Windows DXGI Desktop Duplication**: optional pure Go/syscall backend for Windows 8+ monitor sources (`dxgi:monitor:<adapter>:<output>`). It uses `dxgi.dll`/`d3d11.dll` at runtime and does not replace the Win7-compatible GDI path; unsupported systems or failures fall back to GDI by source choice rather than by default.
- **Linux X11**: pure Go X11 protocol backend using XGB. It supports root/all-screen, RANDR monitor sources, visible EWMH window sources, and XTEST input.
- **Linux framebuffer (`fbdev`)**: pure Go root/KMS-console fallback using `/dev/fb0` or `/dev/fb/0` with `FBIOGET_*` ioctls and read-only `mmap`. Registration only probes geometry/format and skips sysfs-blanked framebuffers; capture mmaps the framebuffer only after a desktop session explicitly selects it. It is view-only, requires framebuffer permissions/root, and works only when the kernel exposes a readable, unblanked framebuffer.
- **Linux DRM/KMS scanout**: pure Go/no-cgo probe and linear scanout mmap fallback using `/dev/dri/card*`, KMS resources, active CRTC framebuffer metadata, `GETFB2`, and PRIME dma-buf export. Registration opens DRM cards read-only and only reports connector/CRTC/framebuffer metadata; PRIME export and mmap are deferred until capture starts. It exposes `drm:<card>:screen:all` and `drm:<card>:connector:<id>` sources when active outputs are visible. This is view-only and best-effort: many accelerated GPU paths expose non-mappable or non-linear scanout buffers, so the backend reports a clear error instead of returning black frames. Full accelerated KMS capture needs an optional native EGL/GBM/DRM-PRIME path.
- **Linux Wayland**: no external helper process is used. The intended backend is native xdg-desktop-portal + PipeWire; until implemented, Wayland reports a clear unavailable reason unless `drm-kms` or `fbdev` fallback is available.
- **macOS**: default no-cgo builds report planned Quartz/CoreGraphics capability metadata. Native capture/control still needs a backend that preserves the portable default build.

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
| Windows | Pure Go Win32/GDI syscall first; enumerates all screens, monitors, visible windows, and optional DXGI monitor sources on Windows 8+ | Pure Go Win32 syscall (`SetCursorPos`, `mouse_event`, `keybd_event`) | GDI remains the default and preserves Win7 compatibility. DXGI Desktop Duplication is an optional monitor-source backend loaded through `dxgi.dll`/`d3d11.dll` at runtime. |
| Linux X11 | Pure Go X11 protocol first; enumerates all screens, RANDR monitors, EWMH client windows, DRM/KMS scanout sources, and fbdev fallback sources; XShm later | Pure Go XTest protocol | Current implementation supports capture and input when X11/XTEST are available. DRM/KMS and fbdev are view-only fallbacks and may fail clearly when the driver exposes non-mappable buffers or a blank framebuffer. |
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

- Add normalized input event schema. Done for pointer move/down/up, wheel, key down, and key up.
- Implement Windows input injection with pure Go Win32 syscalls. Done with Win7-compatible APIs.
- Implement Linux X11 input injection with pure Go XTest protocol. Done when XTEST is available.
- Add permission/error reporting and UI safeguards. Browser control is disabled by default and enabled per session.
- Keep input disabled by default and require explicit opt-in. Done.

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
