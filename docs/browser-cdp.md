# Server Chrome with a device network exit

Open **Browser** in the navigation, select an online device, and choose **Open browser**. The Agent secondary panel's new-tab page has the same Browser entry. Both attach to the browser belonging to the selected device. For a server Agent session, choose an exit device in the panel.

Chrome runs on the rdev server. Its matching, built-in DevTools frontend is served by rdev and embedded with its native Screencast enabled. The frontend provides its own address bar, history navigation, reload, mouse/touch input and developer tools. The rdev toolbar selects page targets, opens new tabs, opens a full DevTools window and closes the browser. This is Chrome's remote rendered view, not reconstructed DOM or a local browser engine.

## Installation

Browser support is disabled by default. Start the server with `--browser` or set `RDEV_BROWSER_ENABLED=true` to enable it. When disabled, navigation and Agent browser entries are hidden, direct UI visits redirect home, and the browser API/CDP endpoints return 404. Installing Chrome or setting `RDEV_CHROME_PATH` alone does not enable the feature.

Install Chrome/Chromium on the server under the unprivileged account running rdev. The server searches `google-chrome-stable`, `google-chrome`, `chromium`, and `chromium-browser`. Set `RDEV_CHROME_PATH=/path/to/chrome` to override discovery. Chrome must be able to start headless with its sandbox enabled; rdev does not add `--no-sandbox`. The frontend assets come from that Chrome binary, so no external DevTools CDN is required.

Update both rdev server and Go clients. The client update makes TCP connection establishment concurrent (up to 64 pending connects) and cancels pending connects on close/disconnect. Older clients still work but can stall resource loads behind slow outbound connects.

## Routing and lifetime

- Each device gets an isolated Chrome process and temporary profile, shared by authorized viewers for that device. At most four instances run concurrently.
- Chrome uses a private loopback HTTP proxy bound to the exact authenticated device connection. HTTP and HTTPS CONNECT reuse rdev's `tcp_connect` tunnel; hostnames are dialed/resolved by the client. Loopback targets also go through the device.
- This uses the **rdev device proxy/network exit**. It does not automatically discover or use a client's Clash/system HTTP/SOCKS configuration. A transparent VPN/TUN on that client applies according to its routing rules.
- QUIC and non-proxied WebRTC UDP are disabled. Device unavailability does not cause the private proxy to fall back to server networking. A disconnected/replaced device's browser is retired within a minute; explicitly opening again after reconnect creates a fresh instance.
- Closing the browser affects all attached viewers and deletes its temporary profile (cookies and tabs). Sessions idle for 30 minutes expire. Server shutdown terminates managed Chrome processes.
- The Chrome debugging port only binds to loopback. Creating/attaching requires the normal API authentication and device password unless an administrator is authenticated. DevTools assets and page sockets use a random, instance-scoped capability URL, revoked on close. These URLs grant control of that browser and should not be shared. The admin token is never included in the embedded DevTools URL or forwarded to Chrome.

## Limitations

The embedded view is the full native DevTools layout; the full-view action is useful on narrow screens. Tabs/cookies do not survive browser shutdown. This increment does not add Agent browser automation tools, persistent profiles, or a video/WebRTC rendering transport.

## Validation (2026-11-09)

- Real server Chrome + real Go client: native inspector assets, Screencast canvas, address navigation, mouse click and keyboard input verified in the embedded frontend. Test page returned `Click verified` and input value `A` after events sent through the native canvas.
- Proxy logs on both ends confirmed the test HTTP request to client loopback traversed the selected device. With the concurrent client, a fresh test navigation completed in 122 ms locally (a functional check, not a performance benchmark).
- Closing returned HTTP 204; the old browser capability immediately returned 404.
- Frontend: 78 files / 300 tests, typecheck and production build passed. Server and CLI suites passed. Client suite and targeted client/server race checks passed.
