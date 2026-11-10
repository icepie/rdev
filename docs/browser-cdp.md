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
- `computer`: `status` (screens/capabilities), `screenshot`, `click`, `drag`, `scroll`, `key`, `text`, `release`. Inputs use the same remote desktop protocol as the manual UI. `text` replaces the client's clipboard and pastes using its platform modifier. GPU-only/view-only clients cannot gain input capabilities through this tool.
- Server-mode conversations must supply `device`; device-bound conversations cannot route to a different device. Existing tool approval rules apply, and offline/password-protected routing follows the same authorization rules as device tools.
- Desktop input requires a screenshot from the same client connection/source/dimensions within two minutes. Coordinates use the returned image pixels. A desktop lease prevents other conversations from interleaving operations; release it explicitly or wait for the two-minute idle expiry.
- The Agent right panel supports multiple terminal, files, desktop and browser workspaces, each with a persisted device selection. Server-mode conversations require choosing a real client for these panels; they never connect a client endpoint to `@server`. The first desktop/browser slot follows tool targets; additional slots remain independent. Selecting another device detaches that slot from the old target until the next tool action.
- Panels are directly interactive, without an Agent-control/takeover bar. Manual legacy desktop input cancels and drains an overlapping Agent operation and invalidates its screenshot; later tools can continue after taking a fresh screenshot. There is no persistent manual-control pause. Independent browser and GPU controls remain authorized native clients.
- Screenshot results use validated JPEG image envelopes (up to 1 MiB encoded), preserving pixel dimensions. They render as image previews and are converted to vision attachments for providers, rather than truncated base64 text. Normal text output limits remain unchanged.

Run the self-contained real Chrome integration with `RDEV_TEST_CHROME=1 go test ./internal/server -run TestBrowserAgentChromeIntegration -count=1`. Its synthetic client only permits a local fixture under a hostname that is unreachable outside the device tunnel. It verifies routing, DOM observation, Unicode fill/click, screenshots, CDP errors and conversation isolation. Desktop tests use a protocol fixture; physical display/input behavior still depends on the client's platform capabilities.


Automation validation: 83 frontend test files / 317 tests, TypeScript check and production build passed. Full `internal/server` and `internal/agent` race suites passed; lifecycle changes additionally passed targeted race checks with real Chrome enabled. Real physical desktop input across OSes was not exercised in this environment.

## GPU 客户端自动化与连接复用

`computer` 根据设备能力选择传输。Go 客户端继续使用普通桌面协议；GPU 客户端使用已经注册的 GPU 桌面隧道，读取 `CapturableList` 并发送原生 Config / PointerEvent / KeyboardEvent / TextInputEvent，不再向 GPU 客户端发送它不支持的 `desktop_start`。

Agent 截图不再依赖服务端 FFmpeg，也不再传输/解码连续视频。Rust GPU 与 Android 通过原有已认证的隧道声明 `AgentCapabilities.version=1`，接收 `GetScreenshot`，返回 `Screenshot`（PNG base64）。服务端检查图片尺寸和大小后转为 JPEG，供视觉模型使用。Rust 在原生捕获工作线程复用 Recorder，Agent Config 使用 `frame_rate=0`，不启动视频编码器。Android 复用 MediaProjection 和 VirtualDisplay，截图临时切到 ImageReader，完成后恢复视频 Surface；不创建第二个投屏授权或重连网页，但已有视频可能短暂停帧。

Go 客户端声明 `desktop.agentVersion=1`，通过 `desktop_start` 的 `onDemand=true` 开启可复用捕获会话，再由 `desktop_snapshot` 请求一帧 JPEG；键鼠仍走轻量控制通道。该公共实现覆盖 Go Windows/macOS/Linux 的已有捕获后端，不改变各平台权限要求或凭空补足不支持的捕获后端。Rust GPU 各构建变体共享原生截图实现；Android 支持原有 MediaProjection 平台范围，输入仍需无障碍权限。

同一 Agent 会话/设备复用控制连接和捕获源，键鼠操作不额外截图。显式释放、普通桌面手动输入、会话删除/重置、连接替换或两分钟空闲关闭 Agent 通道。输入检查截图对应的设备连接、来源与两分钟有效期；操作后需再次截图验证结果，发送成功不等于应用已处理。Go 旧客户端保留原桌面协议回退；GPU/Android 旧客户端缺少原生截图能力时明确提示升级，不再要求安装服务端 FFmpeg。新接口需要同时更新服务端和客户端。

已经打开的 Agent 桌面面板按设备保持稳定组件身份；收到工具动作不重新挂载。GPU 屏幕改变时在原连接上更新 Config，同一屏幕不重复配置。网页查看通道和 Agent 控制通道各自独立、均复用；没有让工具依赖用户浏览器必须在线。

验证：原生 PNG WebSocket/隧道 fixture 验证截图、点击、Unicode、键盘及五次调用仅建立一条连接/一次 Config/两次截图；Go 测试覆盖按需捕获、会话复用、释放与旧协议回退；Rust 测试覆盖像素通道、stride、非法缓冲区和缩放；Android 单元测试覆盖截图不订阅视频。跨平台物理设备仍需实机验收。

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
