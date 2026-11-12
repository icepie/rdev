# Server Chrome with a device network exit

Open **Browser** in the navigation, select an online device, and choose **Open browser**. The Agent secondary panel's new-tab page has the same Browser entry. Both attach to the browser belonging to the selected device. For a server Agent session, choose an exit device in the panel.

Chrome runs on the rdev server. Its matching, built-in DevTools frontend is served by rdev and embedded with its native Screencast enabled. The frontend provides its own address bar, history navigation, reload, mouse/touch input and developer tools. The rdev toolbar selects page targets, opens new tabs, opens a full DevTools window and closes the browser. This is Chrome's remote rendered view, not reconstructed DOM or a local browser engine.

## Installation

Browser support is disabled by default. Start the server with `--browser` or set `RDEV_BROWSER_ENABLED=true` to enable it. When disabled, navigation and Agent browser entries are hidden, direct UI visits redirect home, and the browser API/CDP endpoints return 404. Installing Chrome or setting `RDEV_CHROME_PATH` alone does not enable the feature.

Install Chrome/Chromium on the server under the unprivileged account running rdev. The server searches `google-chrome-stable`, `google-chrome`, `chromium`, and `chromium-browser`. Set `RDEV_CHROME_PATH=/path/to/chrome` to override discovery. Chrome must be able to start headless with its sandbox enabled; rdev does not add `--no-sandbox`. The frontend assets come from that Chrome binary, so no external DevTools CDN is required.

On openSUSE Leap, install `chromium` and `google-noto-sans-cjk-fonts`, then run `fc-cache -f`. Confirm `fc-list :lang=zh` lists a CJK font; a headless Chromium installation alone does not guarantee Chinese glyph coverage. Reopen existing browser sessions after installing fonts if they retain the old font cache.

For an existing root-run rdev service, do not disable Chrome's sandbox or change the service's data permissions just to launch Chrome. The production deployment uses a root-owned `/usr/local/bin/rdev-chromium` launcher: it accepts only a fresh root-owned `/tmp/rdev-browser-<digits>` profile, gives that directory to the dedicated non-login `rdev-browser` account, then uses `setpriv` to drop user/group privileges and capabilities before executing Chromium with a clean environment. `HOME` is the temporary profile; original browser arguments are preserved. The systemd drop-in `/etc/systemd/system/rdev.service.d/chromium.conf` sets `RDEV_CHROME_PATH=/usr/local/bin/rdev-chromium`. Keep the launcher root-owned and never writable by the browser account.

Update both rdev server and Go clients. The client update makes TCP connection establishment concurrent (up to 64 pending connects) and cancels pending connects on close/disconnect. Older clients still work but can stall resource loads behind slow outbound connects.

## Routing and lifetime

- Each device gets an isolated Chrome process and temporary profile, shared by authorized viewers for that device. At most four instances run concurrently.
- Chrome uses a private loopback HTTP proxy bound to the exact authenticated device connection. HTTP and HTTPS CONNECT reuse rdev's `tcp_connect` tunnel; hostnames are dialed/resolved by the client. Loopback targets also go through the device.
- This uses the **rdev device proxy/network exit**. It does not automatically discover or use a client's Clash/system HTTP/SOCKS configuration. A transparent VPN/TUN on that client applies according to its routing rules.
- QUIC and non-proxied WebRTC UDP are disabled. Device unavailability does not cause the private proxy to fall back to server networking. A disconnected/replaced device's browser is retired within a minute; explicitly opening again after reconnect creates a fresh instance.
- Closing the browser affects all attached viewers and deletes its temporary profile (cookies and tabs). Sessions idle for 30 minutes expire. Server shutdown terminates managed Chrome processes.
- The Chrome debugging port only binds to loopback. Creating/attaching requires the normal API authentication and device password unless an administrator is authenticated. DevTools assets and page sockets use a random, instance-scoped capability URL, revoked on close. These URLs grant control of that browser and should not be shared. The admin token is never included in the embedded DevTools URL or forwarded to Chrome.

## Input methods

Click the webpage and type directly. A transparent local textarea at the pointer position hosts OS composition, while `Input.imeSetComposition` mirrors preedit and `Input.insertText` commits the chosen text through the existing CDP session. There is no separate text panel or insert action. Candidate confirmation is not forwarded as a webpage Enter key; ordinary typing, navigation keys and local clipboard paste keep their own paths. Pointer focus changes commit pending composition before the remote click is dispatched.

DevTools follows the rdev interface language (Simplified Chinese or English), including the standalone inspector. Switching languages reloads the inspector frontend, not the remote webpage.

## Limitations

The embedded view defaults to the webpage preview with the DevTools panel collapsed. Use the DevTools button to expand it; the full-view action opens the unmodified native inspector. A small version-dependent adapter uses Chrome's Screencast split widget; unsupported versions fall back to the native layout with a notice. Tabs/cookies do not survive browser shutdown. Persistent profiles and video/WebRTC rendering are not provided. Agent automation is described below.

## Validation (2026-11-09)

- Real server Chrome + real Go client: native inspector assets, Screencast canvas, address navigation, mouse click and keyboard input verified in the embedded frontend. Test page returned `Click verified` and input value `A` after events sent through the native canvas.
- Proxy logs on both ends confirmed the test HTTP request to client loopback traversed the selected device. With the concurrent client, a fresh test navigation completed in 122 ms locally (a functional check, not a performance benchmark).
- Closing returned HTTP 204; the old browser capability immediately returned 404.
- Frontend: 78 files / 300 tests, typecheck and production build passed. Server and CLI suites passed. Client suite and targeted client/server race checks passed.

- Direct input follow-up: real Chrome composition commands targeting the local editing host produced remote preedit `nihao`, committed `你好🌏` once, and one Backspace left `你好`. Ordinary `a` yielded `你好a`; Tab followed by `b` typed into the next field. These are browser-engine composition tests, not a claim of testing every OS input method.
- Chinese DevTools labels verified: 元素、控制台、样式、计算样式、布局. Web suite: 82 files / 313 tests passed; targeted browser backend race checks passed.


## Agent browser and desktop tools

The browser runs **on the server**, with network requests tunneled through the explicitly selected client. This does not open the client's installed Chrome or share its cookies/profile. The `browser` tool is only registered when the server enables `--browser`; `computer` uses the client's real desktop capabilities.

- `browser`: `status`, `open`/`navigate` (HTTP/HTTPS), `snapshot` (bounded page text and CSS selectors), `screenshot`, `click`, `fill`, `scroll`, `key`, `close`. Each conversation/device pair owns a separate page target. Actions never implicitly adopt another conversation's tab. Closing the tool's tab leaves the shared device Chrome process and other tabs intact. Resetting or deleting a conversation retires its owned tabs and desktop lease.
- `computer`: `status` (screens/capabilities), `screenshot`, `click`, `drag`, `scroll`, `key`, `text`, `release`. On CUA-capable clients, input first discovers compatible native SDK schemas and dispatches through the existing CUA session. Unsupported actions retain the remote desktop input path: paired key press/release, Go clipboard paste, or GPU native text input. View-only clients cannot gain input capabilities through this tool.
- Desktop tool selection is CUA-first: check `computer action=status`; when `cua=true`, read the `cua-driver` skill. `cua action=tools` returns a compact catalog; add `tool=<exact name>` to retrieve that tool's complete input schema before `action=call`. Fresh discovery overrides stale capability claims in prior assistant messages. Prefer semantic/accessibility actions over coordinate input. Fall back to `computer` input only when CUA is unsupported or the required action is absent, never to bypass permissions/takeover or replay an uncertain result. Continue using `computer screenshot` for observation and verification. Native Go cgo capability is reported from `desktop.nativeCua`; GPU capability comes from its runtime handshake.
- RDev owns the embedded CUA session; agents must not invent `create_session` or supply session IDs. It does not require the standalone `cua` CLI, `cua-spacesd`, or a Cua cloud account. For a virtual cursor, discover `set_agent_cursor_enabled`, `move_cursor` and `get_agent_cursor_state`, enable and position the overlay in the same session, and verify both state and rendering. The overlay is distinct from the system pointer; `enabled=true` alone is not proof of visibility. Normal desktop cleanup (including turn completion) closes its session, so the overlay does not persist across turns.
- The automatic adapter uses native `press_key`/`type_text`, and pointer actions only when explicit primary-display source geometry matches a fresh SDK `get_desktop_state` capture. It maps image pixels to native capture coordinates, including scaling. GPU name-only sources do not establish primary geometry, so their pointer actions retain the source-aware legacy backend; native key/text can still be preferred. Whole single-axis 120-unit scroll steps are adapted to SDK lines; fractional/dual-axis scroll and unrepresentable keys remain legacy. Missing capability/schema or an unmappable source permits fallback; native errors, permission refusal and uncertain outcomes never replay through another backend.
- Server-mode conversations must supply `device`; device-bound conversations cannot route to a different device. Existing tool approval rules apply, and offline/password-protected routing follows the same authorization rules as device tools.
- Every desktop input (including keys/text and native `cua action=call`) requires a fresh `computer` screenshot from the same client/source within two minutes. One call consumes that observation, even a read-only native call: observe, plan one action, act, then screenshot to verify before continuing. Fallback `computer` pointer coordinates use returned image pixels; native CUA coordinates follow the selected tool's schema and capture geometry, not necessarily those pixels. Both catalog and selected-schema discovery remain available without consuming a screenshot.
- Desktop ownership is released on explicit release, completed/failed/canceled turns, failed desktop operations, human takeover, or two-minute idle expiry. Expiry/replacement drains old input and closes private desktop resources before transferring ownership. A live competing conversation is never silently displaced: the error identifies its thread and remaining lease time. Stop/release that owner or use human takeover, then observe again. Retired conversations cannot regain ownership through delayed tools or resume messages.
- The Agent right panel supports multiple terminal, files, desktop and browser workspaces, each with a persisted device selection. Server-mode conversations require choosing a real client for these panels; they never connect a client endpoint to `@server`. The first desktop/browser slot follows tool targets; additional slots remain independent. Selecting another device detaches that slot from the old target until the next tool action.
- Manual desktop and GPU input cancels and drains overlapping Agent input and invalidates its screenshot. A device-wide reservation remains held through input delivery, excluding even conversations that did not previously own the screen. Later automation must take a fresh screenshot; explicit pause remains paused until resumed. This reuses native desktop/CUA renderers and transports, with no second runtime. GPU capture requests the existing cursor; actual cursor visibility still depends on backend/permissions and must be checked on a real device.
- Screenshot results use validated JPEG image envelopes (up to 1 MiB encoded), preserving pixel dimensions. They render as image previews and are converted to vision attachments for providers, rather than truncated base64 text. Normal text output limits remain unchanged.

Run the self-contained real Chrome integration with `RDEV_TEST_CHROME=1 go test ./internal/server -run TestBrowserAgentChromeIntegration -count=1`. Its synthetic client only permits a local fixture under a hostname that is unreachable outside the device tunnel. It verifies routing, DOM observation, Unicode fill/click, screenshots, CDP errors and conversation isolation. Desktop tests use a protocol fixture; physical display/input behavior still depends on the client's platform capabilities.


Automation validation: 83 frontend test files / 317 tests, TypeScript check and production build passed. Full `internal/server` and `internal/agent` race suites passed; lifecycle changes additionally passed targeted race checks with real Chrome enabled. Real physical desktop input across OSes was not exercised in this environment.

## GPU 客户端自动化与连接复用

`computer` 根据设备能力选择传输。Go 客户端继续使用普通桌面协议；GPU 客户端使用已经注册的 GPU 桌面隧道，读取 `CapturableList` 并发送原生 Config / PointerEvent / KeyboardEvent / TextInputEvent，不再向 GPU 客户端发送它不支持的 `desktop_start`。

Agent 截图不再依赖服务端 FFmpeg，也不再传输/解码连续视频。Rust GPU 与 Android 通过原有已认证的隧道声明 `AgentCapabilities.version=1`，接收 `GetScreenshot`，返回 `Screenshot`（PNG base64）。服务端检查图片尺寸和大小后转为 JPEG，供视觉模型使用。Rust 在原生捕获工作线程复用 Recorder，Agent Config 使用 `frame_rate=0`，不启动视频编码器。Android 复用 MediaProjection 和 VirtualDisplay，截图临时切到 ImageReader，完成后恢复视频 Surface；不创建第二个投屏授权或重连网页，但已有视频可能短暂停帧。

Go 客户端声明 `desktop.agentVersion=1`，通过 `desktop_start` 的 `onDemand=true` 开启可复用捕获会话，再由 `desktop_snapshot` 请求一帧 JPEG；键鼠仍走轻量控制通道。该公共实现覆盖 Go Windows/macOS/Linux 的已有捕获后端，不改变各平台权限要求或凭空补足不支持的捕获后端。Rust GPU 各构建变体共享原生截图实现；Android 支持原有 MediaProjection 平台范围，输入仍需无障碍权限。

同一 Agent 会话/设备复用控制连接和捕获源。CUA 坐标适配会额外调用 SDK 桌面观察来校验主屏尺寸并重置内部缩放；该观察不替代用户可见的截图前置条件。每个输入会消耗最近截图，下一次输入前必须再次截图并验证，包括键盘、文字和原生 CUA 调用。显式释放、手动输入、回合完成/失败/取消、桌面操作失败、会话删除/重置、连接替换或两分钟空闲关闭 Agent 通道。过期资源先等待输入释放并关闭，再允许新拥有者进入。Go 旧客户端保留原桌面协议回退；GPU/Android 旧客户端缺少原生截图能力时明确提示升级。新接口需要同时更新服务端和客户端。

已经打开的 Agent 桌面面板按设备保持稳定组件身份；收到工具动作不重新挂载。GPU 屏幕改变时在原连接上更新 Config，同一屏幕不重复配置。网页查看通道和 Agent 控制通道各自独立、均复用；没有让工具依赖用户浏览器必须在线。

已通过完整服务端测试及 `go test -race ./internal/server -run 'Test(Goal|Loop|Autonomy|Automation|ManualInput|Computer|GPUDesktop|Autojs|AgentCua)' -count=1`。真实 Go 客户端在隔离 X11 桌面完成截图→点击→拒绝无新截图的重复点击→截图→释放，外部读取光标位置确认实际输入。Rust/GPU 实机尚未验证；仍需分别在目标平台检查原生光标、Unicode、配对按键释放、手动抢占、取消、两个会话竞争、两分钟自动释放，以及旧会话不能恢复控制。发送成功不是应用结果确认。

CUA 优先适配另经隔离 Xvfb 实跑：真实 Linux CUA bridge 完成 tools→get_desktop_state→click，XQueryPointer 读回目标坐标 (123,234)。自动适配与权限/新鲜度/错误不重放的服务端回归及 race 测试通过；此验证不覆盖 Windows/macOS 或 Rust GPU 实机输入。

## 服务端 Agent 对话管理

仅服务端 Agent 提供 `agent_sessions`（list/read，历史支持 offset/limit）和 `manage_agent_session`（create/rename/delete，必须指定 device）。离线设备已有历史也可管理，权限检查沿用跨设备会话管理规则；受保护设备及离线跨设备管理需要管理员认证。修改操作沿用工具审批，删除复用 UI 的取消工作、持久化清理和广播流程，禁止删除正在执行该工具的调用方对话。管理不会向目标会话发送消息，也不会切换当前浏览器聊天视图。

右侧每个文件管理页签的传输记录独立存储。跨设备文件预览通过该设备已授权的 Files 连接读取，请求和回复同时校验设备及请求 ID，不使用服务端会话的同名文件路径。

## Browser interaction alignment (2026-11-10)

Compared against `shiwenwen/hope-agent` at `c230286`, especially `crates/ha-browser/src/tool/mod.rs` and `docs/architecture/core/browser.md`. This increment keeps RDev's existing tool schema and server Chrome/device-network architecture:

- `hover`: selector or screenshot coordinates, dispatching a native CDP mouse move.
- `select`: native HTML select by `selector` and option value in `text`; rejects missing/disabled options and dispatches input/change events. Custom combobox widgets still use snapshot/click/key.
- `wait_for`: poll a selector until visible, bounded by the existing operation timeout and caller cancellation. No unbounded sleeps.
- Snapshots include associated/ARIA labels, current non-password values, checked/disabled state and up to 30 options per select, within the existing 10 KB output bound.
- Selector-based click/hover rejects ambiguous, hidden, disabled or covered targets, and clicks the visible portion after scrolling. Models should take another snapshot after actions to verify the page effect.

The new operations publish through the existing conversation-scoped automation events and retain the panel's existing hide/reveal behavior. This is not full Hope parity: stable element references, multi-tab management within one conversation, frame/shadow-root traversal, runtime observation, uploads and downloads remain separate work.

Validation: DOM behavior tests plus real Chrome integration for route isolation, fill/click/select/hover, delayed elements, wait cancellation and recovery, and invalid option rejection. These tests use local fixtures, not arbitrary production websites.

## Viewport controls

Preview controls provide width/height inputs and common dimensions. Right, bottom
and corner grips sit directly on the preview surface, like DevTools device mode.
Dragging accounts for the current preview scale and changes the remote viewport
through the existing CDP connection, bounded to 240–3840 by 240–2160 CSS pixels.
Dimensions are retained per target in sessionStorage without capability URLs.

Step replay has been removed, including timeline UI, thumbnail collection,
server history caches and protocol fields. Explicit screenshot requests remain.
