# Agent interaction verification — 2026-11-08

References: BB `f51e6b0`, OMP `75e7801`, Hope `2b9f809`.

## Automated checks

- Web: 44 files, 133 tests passed; TypeScript passed; production Vite build passed.
- `go test ./...` passed.
- Race checks: `internal/client`, `internal/agent`, and `internal/server` passed. An initial server race build conflicted with Vite replacing embedded assets; rerunning after assets were stable passed.
- New coverage: canceling a shell stops its process-group child while another session completes; final output flush waits for an in-flight send; output order is preserved; unread survives reload; streaming scroll calls coalesce; presentation metadata survives storage but is omitted from provider JSON; image preview bounds/storage; canvas validation and sandbox attributes; navigation targets the correct pane.

## Browser evidence

Used a separate local server at port 19440, isolated browser context, seeded review history and a local SSE model fixture. This is protocol/UI verification, not genuine model task-quality evidence.

- 390×844 light: message navigation shows Agent/user indexes with BB rail and popover layout; no root horizontal overflow.
- Sent-image thumbnail, read-file image preview and enlargement dialog rendered successfully. Mobile image preview uses the shared bottom-sheet behavior.
- Expanded HTML canvas appears in an iframe with `sandbox="allow-scripts"`; its Check button accepts a native browser click. CSP disallows network and form submission. Source and copy controls are present.
- 1440×1000 dark: no root horizontal overflow; font is `"Inter Variable", Inter, sans-serif`; background is BB `oklch(0.195 0 0)` (light: `oklch(1 0 0)`). Default light/dark token blocks were compared with the BB reference: no missing or differing values.
- Server-provided work duration and Steer markers survive history replay.
- Native browser fill + Enter sent a message, cleared the draft, preserved textarea focus and remained on `/agent`.
- Native `/compact preserve device IDs` completed, replaced history, displayed the completion feedback, cleared the draft and preserved textarea focus.
- Expanded historical tool bodies were found too dim during review; their opacity now returns to normal when expanded.

## Boundaries

No production deployment occurred. The original full-resolution sent image is not persisted; the bounded preview is. Older history cannot recover absent timestamps or image data. Windows currently cancels only the direct non-PTY process; Unix process-group tests ran on Linux, not macOS hardware. The canvas is an isolated retained call preview, not Hope's complete artifact/version/export platform. Hardware GPU capture and the complete active-state animation/overlay matrix remain separate acceptance work.

## Follow-up: desktop isolation and durable outcomes

- Real isolated Xvfb `:98` desktop (1024×768) streamed through client/server/browser; alternating green/blue pixels confirmed successive frames. Mobile 390×844 rendered the stream at 342×256 without root overflow. A native canvas click moved the isolated X pointer from (50,50) to (512,384). The actual user desktop was not accessed.
- Two real device clients requested background command approval concurrently. Allowing device A left device B pending; rejecting B affected only B. A completed and rendered plain output with exit code 0, without raw stdout tags.
- GPU global IME/paste now requires the matching focused overlay; pointer hover no longer steals focus. Blur releases held keys. Unit coverage checks two panes, composer focus, inert and disconnected overlays; this is not a hardware GPU input test.
- Foreground outcome records survive reopen/rewrite; reset removes them. A persisted running marker restores as stopped after restart. Full server race suite passed; focused outcome and desktop error protocol race tests passed after the final retryable-error change.
- Corrected missing icon names for canvas, errors, loading and BB Steer (CornerDownRight).
- Browser reconnect verification: stopped the isolated desktop client, observed repeated offline errors with `retryable: true` and increasing retry delays, restarted the client, and observed automatic `ready` plus a resumed stream without clicking Connect.
- Clicking Disconnect kept the desktop closed with no new WebSocket connection attempts during the subsequent observation.

## Follow-up: completion during disconnection

- Added persisted `outcomeAt` to thread lists, including disk-only devices. It is independent of general metadata updates and survives history rewrite/restart. Starting the next turn preserves the last completion marker; reset clears it.
- Hook regression coverage reconnects after an entire unobserved turn, distinguishes same-named threads on two devices, clears only the visible conversation, and excludes rename/reset/initial-history false positives. Existing tab-scoped unread storage remains readable.
- Real WebSocket attach regression verifies a completed turn exposes a valid completion marker to a new connection. Full server race suite and targeted attach/history race tests pass; Web tests and TypeScript pass.
