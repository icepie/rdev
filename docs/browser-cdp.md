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

The embedded view defaults to the webpage preview with the DevTools panel collapsed. Use the DevTools button to expand it; the full-view action opens the unmodified native inspector. A small version-dependent adapter uses Chrome's Screencast split widget; unsupported versions fall back to the native layout with a notice. Tabs/cookies do not survive browser shutdown. This increment does not add Agent browser automation tools, persistent profiles, or a video/WebRTC rendering transport.

## Validation (2026-11-09)

- Real server Chrome + real Go client: native inspector assets, Screencast canvas, address navigation, mouse click and keyboard input verified in the embedded frontend. Test page returned `Click verified` and input value `A` after events sent through the native canvas.
- Proxy logs on both ends confirmed the test HTTP request to client loopback traversed the selected device. With the concurrent client, a fresh test navigation completed in 122 ms locally (a functional check, not a performance benchmark).
- Closing returned HTTP 204; the old browser capability immediately returned 404.
- Frontend: 78 files / 300 tests, typecheck and production build passed. Server and CLI suites passed. Client suite and targeted client/server race checks passed.

- Direct input follow-up: real Chrome composition commands targeting the local editing host produced remote preedit `nihao`, committed `你好🌏` once, and one Backspace left `你好`. Ordinary `a` yielded `你好a`; Tab followed by `b` typed into the next field. These are browser-engine composition tests, not a claim of testing every OS input method.
- Chinese DevTools labels verified: 元素、控制台、样式、计算样式、布局. Web suite: 82 files / 313 tests passed; targeted browser backend race checks passed.
