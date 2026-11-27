# Android Controlled Endpoint APK Design

This design is for a native Android controlled endpoint. It does not depend on Termux; the Termux-hosted CLI clients are documented in [docs/android-termux.md](android-termux.md).

The APK is released. The dashboard Android tab (`web/ui/pages/Dashboard.tsx`) links `/download-release-proxy?asset=rdev-android-apk-debug.apk&tag=latest`, which serves the newest signed `rdev-android-apk-debug.apk` release asset. `.github/workflows/android-apk.yml` runs the Android unit tests and `assembleDebug`, stages `dist/rdev-android-apk-debug.apk` as the `rdev-android-apk-debug` artifact, and on `v*` tags signs the APK with `go run ./scripts/sign` and attaches it to the release. The rest of this page describes the shipped implementation; the run steps are in [Run Steps](#run-steps).

## Goals

- Support broad Android compatibility while keeping the high-performance path first.
- Require Android 7.0 / API 24 for the embedded AutoJs6 runtime and bundled OpenCV.
- Keep RDev's native Android UI, capture and input paths; compile the genuine AutoJs6 runtime into the same APK.
- Reuse RDev server authentication, device registration, browser desktop UI, and `gpu-desktop-tunnel` routing where possible.

## Compatibility Targets

| Area | Target |
| --- | --- |
| Minimum OS | Android 7.0 / API 24 |
| Preferred OS | Android 8+ for better `MediaCodec` stability |
| CPU ABI | Universal APK: arm64-v8a, armeabi-v7a, x86, x86_64; includes native OpenCV |
| Capture | `MediaProjection` + `VirtualDisplay` |
| Encode | `MediaCodec` H.264 baseline/main (HEVC not implemented) |
| Input | `AccessibilityService` |
| Background | Foreground service notification |
| Terminal | In-app PTY (`AndroidTerminalManager`), SFTP and file manager (`AndroidFileManager`, `AndroidSftpSession`) |

## Architecture

```text
Browser desktop UI
       │
       │ /gpu-desktop/<device>/ws
       ▼
RDev server
       │
       │ /ws registration + /gpu-desktop-tunnel raw stream proxy
       ▼
Android APK
  ├─ MainActivity
  ├─ RDevAgentService
  ├─ RDevWebSocketClient
  ├─ RDevGpuTunnel
  ├─ ScreenCapturePipeline
  ├─ VideoEncoderPipeline
  ├─ RDevAccessibilityService
  ├─ AndroidTerminalManager
  ├─ AndroidFileManager + AndroidSftpSession
  └─ AutoJs6 runtime + RDevAutoJs + OpenCV
```

## Components

### MainActivity

- Stores server URL, device ID, and optional password.
- Requests `MediaProjection` permission.
- Opens Android Accessibility settings.
- Starts/stops `RDevAgentService`.
- Shows connection, capture, and input permission status.

### RDevAgentService

- Runs as a foreground service (`android:foregroundServiceType="mediaProjection"`).
- Connects to RDev server `/ws`.
- Registers with the existing RDev JSON protocol (`clientId`, `displayName`, `instanceId`, `deviceIdentity`, `reconnectProof`, `password`, `desktop`, `fileOperations`); the server's `register` reply carries the final device ID, which `startTunnel()` hands to `RDevGpuTunnel`.
- Reports Android desktop capability; `input`/`viewOnly` follow `RDevAccessibilityService.isActive()`:

```json
{
  "platform": "android",
  "displayServer": "mediaprojection",
  "agentVersion": 1,
  "supported": true,
  "input": true,
  "viewOnly": false,
  "clipboard": false,
  "backends": ["android-mediaprojection", "gpu-desktop-tunnel"],
  "inputBackends": ["android-accessibility"],
  "videoCodecs": ["h264"],
  "encoderBackends": ["mediacodec"]
}
```

- Starts capture only on viewer demand: `desktop_start` marks the session active, and capture pauses 3 seconds after the last desktop viewer goes away.
- Answers desktop commands in-process: `desktop_ready` carries capture size and `format=h264`; `new_session`/`stdin_close`/`resize` drive the PTY; `file_*` drive `AndroidFileManager`; `tcp_connect`/`tcp_close` drive port forwarding.

### RDevGpuTunnel

Current desktop transport (`clients/rdev-android/app/src/main/java/dev/icepie/rdev/RDevGpuTunnel.java`), constructed by `RDevAgentService.startTunnel()` after registration.

- Opens one outbound WebSocket to `<ws|wss>://<authority>/gpu-desktop-tunnel?device=<id>&deviceIdentity=<identity>&reconnectProof=<proof>[&password=<password>]`. An unknown device is rejected with HTTP `404`, a missing or invalid reconnect proof with `401`.
- Multiplexes every desktop HTTP request and upgraded WebSocket over that connection. The APK opens no listening port; the server keeps proxying `/gpu-desktop/<device>/...` into the tunnel and only authenticates and forwards bytes.
- Frame header: `[1 byte frameType][8 bytes streamId big endian][payload]`; `1` opens a stream with the browser's HTTP request head, `2` carries data, `3` closes the stream.
- The first bytes of a stream are parsed as HTTP: a plain request gets a short `200` body and the stream closes; `Upgrade: websocket` answers `101` with `Sec-WebSocket-Accept`, then the RDev desktop protocol runs in-process (`GetCapturableList`, `ResumeVideo`/`PauseVideo`, `GetScreenshot`, `PointerEvent`, `WheelEvent`, `TextInputEvent`/`KeyboardEvent`, clipboard, `Config`, `AutoJs`).
- Video: `ScreenCapturePipeline` → MediaCodec → `Fmp4Muxer`, batched onto the tunnel stream; each stream tracks SPS/PPS, waits for a keyframe after reconnect, and applies `Config` FPS/quality.
- A `tcp://`, `kcp://`, or `udp://` server entry switches the tunnel to `RDevTcpClient`/`RDevKcpClient` with the same 9-byte framing.
- Reconnects with jittered exponential backoff from 1 s up to 30 s.

History: the original plan ran a loopback HTTP/WebSocket desktop service inside the APK, mirroring embedded `rdev-desktop` on Rust packages. The shipped APK has no loopback listener: this tunnel serves the same HTTP/WebSocket protocol over the outbound connection, so the server and browser path stay identical.

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

Browser path: the APK sends fragmented MP4 (`Fmp4Muxer`) and the existing browser player consumes it through MSE, with WebCodecs where the browser supports it. The APK advertises `h264` only.

### Fallback: JPEG (not implemented)

Planned pipeline, kept for history:

```text
MediaProjection → ImageReader → Bitmap/YUV conversion → JPEG → existing desktop frame path
```

The shipped APK has no JPEG capture path; an H.264 encoder is required for the Android stream. Devices without one stay at the desktop's capability error instead of a JPEG fallback.

## Protocol Plan

### Phase 1: APK Registration (implemented)

- Reuse existing `/ws` registration.
- Add Android capabilities to `DesktopCapabilities` values only; no server protocol change required initially.
- Start GPU tunnel only after receiving server register response and final device ID.
- WebSocket compression must leave the client window at 15 bits (32 KiB). With context takeover enabled, gws otherwise defaults to 12 and emits `client_max_window_bits=12`, which OkHttp rejects before sending registration. Compressed server upgraders set `ClientMaxWindowBits: 15`; keep that value and the existing server window. A successful HTTP 101 alone does not prove registration: verify the device list and a remote shell command.

### Phase 2: GPU Desktop Tunnel (implemented)

- `RDevGpuTunnel` opens `/gpu-desktop-tunnel` and serves the desktop HTTP/WebSocket protocol in-process; there is no loopback listener.
- WebSocket URL normalization adds `/ws` only to a server root. Preserve explicit endpoint paths and encoded query parameters, especially `/gpu-desktop-tunnel`; appending `/ws` to that tunnel produces HTTP 404 even when registration and screen-capture permission succeed.
- The tunnel uses the same 9-byte frame header:

```text
[1 byte frameType] [8 bytes streamId big endian] [payload]
```

- This lets the server keep using its existing HTTP/WebSocket proxy path.

### Phase 3: Video Transport (implemented)

MediaCodec H.264 access units are muxed to fragmented MP4 (`Fmp4Muxer`) and pushed over the existing desktop WebSocket inside the tunnel, so the browser decodes them through its existing MSE/WebCodecs path. No Android-specific server message types were added, and all streaming stays under `/gpu-desktop/<device>/...`. The APK advertises `h264` only; the JPEG fallback remains a browser-side path for other clients.

### Phase 4: Input Transport (implemented)

- Browser sends existing desktop pointer/keyboard events.
- Server forwards them through the tunnel to the APK.
- `RDevGpuTunnel` decodes `PointerEvent`, `WheelEvent`, and `TextInputEvent`/`KeyboardEvent` and maps them to Accessibility gestures/actions through `RDevAccessibilityService`.

## Run Steps

1. Install `rdev-android-apk-debug.apk` — dashboard → Android tab → **Download APK**, or the GitHub release asset of the same name.
2. Open **RDev Android** and fill **服务器** (`wss://...`; `http(s)://` is upgraded), **设备 ID**, optional **显示名称**, optional **访问密码**; tap **保存配置**. An `rdev://connect?...` link fills the same fields.
3. Tap **启动在线** to start the foreground service. It registers over `/ws`, immediately opens `/gpu-desktop-tunnel`, and the device appears in the dashboard device list.
4. Tap **授权桌面** to grant `MediaProjection`. Capture runs only while a desktop viewer is attached and pauses 3 seconds after the last viewer leaves.
5. Grant **触控 / AutoJs 无障碍权限** to enable **RDev Remote Input** (tap/swipe/text/system keys) and **RDev AutoJs Scripts** (the `autojs` tool). Without it the device registers `input=false`, `viewOnly=true`.
6. Optional: **键盘设置** / **切换键盘** for `RDevInputMethodService`, **文件权限** for the all-files access SFTP needs, **后台保活** to exempt the service from battery optimization.

## Embedded AutoJs6

RDev Android 0.2.129 embeds AutoJs6 6.8.0 from the pinned `third_party/autojs6` submodule. It preserves upstream application initialization, Rhino, script modules and the AutoJs accessibility service. No separate AutoJs application is required. OpenCV 4.8.0 is bundled from `third_party/autojs6-opencv`, verified against its native provenance hashes, and checked for 16 KiB page alignment during the build.

The Agent `autojs` tool runs on the selected Android device through the authenticated GPU tunnel:

```json
{"action":"status"}
```

```json
{"action":"run","script":"auto(); JSON.stringify({package: currentPackage(), buttons: className('android.widget.Button').find().size()});","timeoutMs":5000}
```

- In a server conversation, add an explicit `device` selected with `list_devices`. The tool shares desktop ownership with `computer`; manual takeover or closing the execution stream cancels the running engine.
- `status` reports the engine version and AutoJs accessibility connection. Enable **RDev AutoJs Scripts** for selectors, clicks and gestures. `requestScreenCapture()` still requires Android's system consent; it is not silently granted.
- `run` accepts background Rhino scripts, up to 65,536 UTF-8 bytes. UI-mode and external-engine scripts are rejected. Optional upstream plugins are not all bundled; this integration includes OpenCV, not Node/Python/OCR plugin runtimes.
- The timeout defaults to 25 seconds and cannot exceed 25 seconds. Results include `status`, the last expression converted to text, `logs`, and any `error`. Use `JSON.stringify` for structured values. Text/error fields are bounded to 8 KiB, logs to 48 KiB/256 lines, and the response to 128 KiB; `truncated` indicates discarded output.
- Upstream external script launch/import activities are not exported. Required Android system-permission-protected IPC remains available. AutoJs6's MPL-2.0 license, source links and OpenCV notices are packaged under `assets/licenses/`.
- Desktop commands such as `"GetCapturableList"`, `"ResumeVideo"`, `"PauseVideo"` and `"GetScreenshot"` are JSON strings; AutoJs requests are JSON objects. Decode either JSON value type before dispatching. Android 0.2.130 fixes the 0.2.129 regression that rejected string commands before video subscription, leaving a connected viewer at zero frames.

Verified on an Android 15 x86_64 emulator: real Rhino execution and console logs, file round-trips, accessibility queries and clicks, system-consented 1080×2400 capture, OpenCV resizing/matrix operations, permission errors, exceptions, output truncation, timeout, and cancellation preventing later file writes. Android 0.2.130 was also verified on a physical arm64 Samsung SM-G9650: the production browser displayed the 464×960 screen stream, and the same desktop tunnel answered both the string screen-list command and the AutoJs status object. The remaining ABIs are packaged and alignment-checked, not device-tested.

## ADB Development Flow

Use JDK 21 and an Android SDK with accepted licenses. The pinned upstream wrapper supplies Gradle; required SDK/NDK components are resolved by the Android build. The overlay keeps the upstream checkout unmodified and uses its pinned shared dependencies.

For headless builds, point `JAVA_HOME` at a regular JDK 21 distribution (verified
with Temurin), not Android Studio's JBR. The upstream platform plugin can identify
JBR as IntelliJ IDEA version `0` outside the IDE and reject it. A newer system JDK
can instead trigger an additional toolchain download; using the installed JDK 21
avoids both issues without bypassing the upstream compatibility checks.

```sh
git submodule update --init --recursive
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
sh third_party/autojs6/gradlew -p clients/rdev-android --no-daemon assembleDebug
adb devices
adb install -r clients/rdev-android/app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n dev.icepie.rdev/.MainActivity
adb logcat -s RDevAgent RDevTunnel RDevCapture RDevEncoder RDevInput RDevAutoJs RDevTerm RDevSftp RDevFiles RDevIME
```

Run the existing RDev Android unit tests with `sh third_party/autojs6/gradlew -p clients/rdev-android --no-daemon test`; `.github/workflows/android-apk.yml` runs that task, then `assembleDebug`, with JDK 21 before staging the APK.

On an isolated test device, enable both accessibility services if the ROM allows it. This command replaces the enabled-service list; do not use it unchanged on a personal device:

```sh
adb shell settings put secure enabled_accessibility_services dev.icepie.rdev/org.autojs.autojs.core.accessibility.AccessibilityServiceUsher:dev.icepie.rdev/dev.icepie.rdev.RDevAccessibilityService
adb shell settings put secure accessibility_enabled 1
```

## Implementation Milestones

Original plan, kept for history; each phase is marked with its shipped state, and the component sections above are the reference.

1. **APK shell** — shipped: MainActivity, foreground service, persisted config, `/ws` registration.
2. **Tunnel** — shipped: `RDevGpuTunnel` multiplexes the desktop HTTP/WebSocket protocol over `/gpu-desktop-tunnel` after registration.
3. **Capture** — shipped: MediaProjection + VirtualDisplay lifecycle with rotation/restart handling and viewer-driven start/stop.
4. **Encode** — shipped: MediaCodec H.264 surface encoder with SPS/PPS config emission and keyframe control.
5. **Browser** — shipped: Android video is fragmented MP4 over the existing MSE/WebCodecs path; the APK advertises H.264 only.
6. **Input** — shipped: Accessibility gestures and system actions.
7. **Release** — shipped: GitHub Actions builds the APK artifact, `v*` tags publish signed assets, and the dashboard Android tab downloads them.

## Historical Open Decisions

These were open when the plan was written; the shipped implementation settled them.

- Video transport: fragmented MP4 over the existing desktop WebSocket (MSE/WebCodecs in the browser). No WebCodecs Annex-B/AVCC path was added.
- Tunnel protocol: the 9-byte multiplexed tunnel above, not an emulated embedded `rdev-desktop` socket.
- HEVC: not implemented; the APK advertises H.264 only.
- Device Owner helpers: not implemented.
