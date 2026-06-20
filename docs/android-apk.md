# Android Controlled Endpoint APK Design

This design is for a native Android controlled endpoint. It does not depend on Termux.

## Goals

- Support broad Android compatibility while keeping the high-performance path first.
- Start with Android 5.0 / API 21 because `MediaProjection` is introduced there.
- Use system APIs and Java/Kotlin only for the first implementation; avoid Compose/AndroidX-heavy dependencies.
- Reuse RDev server authentication, device registration, browser desktop UI, and `gpu-desktop-tunnel` routing where possible.

## Compatibility Targets

| Area | Target |
| --- | --- |
| Minimum OS | Android 5.0 / API 21 |
| Preferred OS | Android 8+ for better `MediaCodec` stability |
| CPU ABI | APK is Java-only first; no ABI split required |
| Capture | `MediaProjection` + `VirtualDisplay` |
| Encode | `MediaCodec` H.264 baseline/main; optional HEVC on supported devices |
| Input | `AccessibilityService` |
| Background | Foreground service notification |
| Terminal | Out of scope for APK MVP |

## Architecture

```text
Browser desktop UI
       │
       │ /desktop.html + /gpu-desktop/<device>/...
       ▼
RDev server
       │
       │ /ws registration + /gpu-desktop-tunnel raw stream proxy
       ▼
Android APK
  ├─ MainActivity
  ├─ RDevAgentService
  ├─ RDevWebSocketClient
  ├─ AndroidGpuDesktopService
  ├─ ScreenCapturePipeline
  ├─ VideoEncoderPipeline
  └─ RDevAccessibilityService
```

## Components

### MainActivity

- Stores server URL, device ID, and optional password.
- Requests `MediaProjection` permission.
- Opens Android Accessibility settings.
- Starts/stops `RDevAgentService`.
- Shows connection, capture, and input permission status.

### RDevAgentService

- Runs as a foreground service.
- Connects to RDev server `/ws`.
- Registers the device using the existing RDev JSON protocol.
- Reports Android desktop capability:

```json
{
  "platform": "android",
  "supported": true,
  "input": true,
  "viewOnly": false,
  "backends": ["android-mediaprojection", "gpu-desktop-tunnel"],
  "inputBackends": ["android-accessibility"],
  "videoCodecs": ["h264", "jpeg"],
  "encoderBackends": ["mediacodec"]
}
```

### AndroidGpuDesktopService

- Exposes a local loopback service inside the APK process, similar to embedded `rdev-desktop` on Rust client packages.
- The APK connects `/gpu-desktop-tunnel?device=...&instanceId=...` after `/ws` registration succeeds.
- Server proxies `/gpu-desktop/<device>/...` to this local Android desktop service.
- This keeps the browser/server side close to the existing Rust GPU desktop model.

### ScreenCapturePipeline

- Uses `MediaProjection.createVirtualDisplay()`.
- Feeds frames directly into `MediaCodec` input `Surface` for the high-performance path.
- Avoids CPU readback in the default path.
- Recreates the virtual display on rotation, resolution changes, or encoder restart.

### VideoEncoderPipeline

- Uses `MediaCodec` H.264 encoder with surface input.
- Starts with AVC Baseline or Main profile for broad browser compatibility.
- Emits initialization/config data when encoder format changes.
- Emits encoded samples with keyframe flags and timestamps.
- Requests IDR frames on reconnect, stream start, and decoder failure.

### RDevAccessibilityService

- Handles remote input events from RDev desktop UI.
- Supports tap, long press, swipe, drag, text input, Back, Home, Recents, and notification shade where supported.
- Requires user-granted Accessibility permission.
- If permission is missing, APK remains view-only and reports `input=false` or input backend reason.

## Streaming Strategy

### Preferred: H.264 MediaCodec

Pipeline:

```text
MediaProjection → VirtualDisplay → MediaCodec input Surface → H.264 access units → RDev stream → Browser decoder
```

Benefits:

- Minimal CPU copy.
- Hardware encoding on most Android devices.
- Lower latency and bandwidth than JPEG/MJPEG.
- Matches RustDesk-style architecture.

Browser path options:

1. **MSE/fMP4**: package H.264 into fragmented MP4 and append to `MediaSource`.
2. **WebCodecs**: send Annex-B/AVCC samples with metadata and decode with `VideoDecoder` where available.
3. **Fallback JPEG**: only for old browsers or devices without usable H.264 encoder.

Recommended first implementation: MSE/fMP4 if we can reuse the current GPU desktop browser video path; otherwise add WebCodecs first because it can consume discrete encoded frames with lower packaging complexity.

### Fallback: JPEG

Pipeline:

```text
MediaProjection → ImageReader → Bitmap/YUV conversion → JPEG → existing desktop frame path
```

Use only when:

- No H.264 encoder is available.
- Browser has no usable video decoder path.
- Debugging capture before MediaCodec integration.

## Protocol Plan

### Phase 1: APK Registration

- Reuse existing `/ws` registration.
- Add Android capabilities to `DesktopCapabilities` values only; no server protocol change required initially.
- Start GPU tunnel only after receiving server register response and final device ID.

### Phase 2: GPU Desktop Tunnel

- APK implements local Android desktop service over loopback.
- APK opens server `/gpu-desktop-tunnel` using the same 9-byte tunnel frame header:

```text
[1 byte frameType] [8 bytes streamId big endian] [payload]
```

- This lets server keep using its existing HTTP/WebSocket proxy path.

### Phase 3: Video Transport

Use one of these options:

- Reuse current GPU desktop websocket frontend protocol if it accepts MP4 chunks.
- Add Android-specific `NewVideo`, `VideoConfig`, and binary chunk messages to the tunneled websocket.
- Keep all video streaming under `/gpu-desktop/<device>/...` so existing UI routing still works.

### Phase 4: Input Transport

- Browser sends existing desktop pointer/keyboard events.
- Server forwards through GPU desktop websocket to APK local service.
- APK maps events to Accessibility gestures/actions.

## Permission UX

1. User installs APK.
2. User opens APK and enters server URL/password/device ID.
3. APK asks for notification permission on Android 13+.
4. APK asks for screen capture permission with `MediaProjectionManager`.
5. APK guides user to enable Accessibility.
6. User taps Start; foreground service connects and registers.

## ADB Development Flow

```sh
adb devices
./gradlew -p clients/rdev-android assembleDebug
adb install -r clients/rdev-android/app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n cn.singzer.rdev.android/.MainActivity
adb logcat -s RDevAndroid RDevAgent RDevCapture RDevEncoder RDevInput
```

Enable Accessibility on a test device if the ROM allows it:

```sh
adb shell settings put secure enabled_accessibility_services cn.singzer.rdev.android/.RDevAccessibilityService
adb shell settings put secure accessibility_enabled 1
```

## Implementation Milestones

1. **APK shell**: MainActivity, foreground service, persisted config, `/ws` registration.
2. **Tunnel**: APK connects `/gpu-desktop-tunnel` after registration and serves local HTTP/WebSocket desktop endpoint.
3. **Capture**: MediaProjection + VirtualDisplay lifecycle with rotation/restart handling.
4. **Encode**: MediaCodec H.264 surface encoder, SPS/PPS/config emission, keyframe control.
5. **Browser**: MSE/WebCodecs decode path for Android stream, with JPEG fallback hidden behind capability detection.
6. **Input**: Accessibility gestures and system actions.
7. **Release**: GitHub Actions builds signed/unsigned APK artifacts, one-click APK link from server UI.

## Open Decisions

- Whether Android video stream should use current MSE/fMP4 path or a new WebCodecs Annex-B/AVCC path.
- Whether to add a dedicated Android desktop websocket protocol or exactly emulate current embedded `rdev-desktop` protocol.
- Whether to support optional HEVC for LAN/high-efficiency cases after H.264 is stable.
- Whether to provide Device Owner helpers for kiosk/enterprise deployments.
