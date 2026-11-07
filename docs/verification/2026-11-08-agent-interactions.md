# Agent interaction verification — 2026-11-08

References: BB `f51e6b0`, OMP `75e7801`, Hope `2b9f809`.

## Automated checks

- Web: 47 files, 139 tests passed; TypeScript passed; production Vite build passed.
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

## Follow-up: protocol boundary and responsive overlays

- Found and corrected the `parseThreads` boundary dropping `outcomeAt`. Unread regression fixtures now pass through `parseAllThreads`, covering the actual decoding path rather than constructing final UI rows directly.
- Offline Agent sockets now close with retryable 1013; permanent setup failures still stop retrying. Explicit device URLs connect without waiting for the device to appear in discovery. A native browser reload while the isolated client was stopped retained `desktop-review`, disabled the composer and showed reconnecting.
- Agent transcript containing real background output and highlighted fixture Markdown checked at 390/768/1440 × light/dark. All six combinations had root width equal to viewport width, Inter Variable font, BB white/light and oklch(0.195 0 0)/dark backgrounds, and a contained enabled composer when connected. Reduced-motion mode reported zero running animations after settling; menu animation duration was 0.00001s.
- Mobile light screenshot reviewed code blocks, tool rows, composer and footer. Native menu opening/Escape exposed lost focus; pointer activation now establishes its trigger as the return target. The drawer shell remains mounted if closed before deferred content realizes, allowing its focus cleanup to run. Regression covers immediate open/Escape; native browser confirms focus returns to the conversation action button with aria-expanded=false.
- 134 Web tests, TypeScript and production build passed. Targeted Agent reconnect/attach tests passed with the race detector. This covers the listed idle/menu states, not every streaming/approval/split/overlay combination.
- Restarting the isolated client restored the same Agent device and transcript automatically; the composer re-enabled and reconnecting cleared without user action.

## Follow-up: navigation rail in split panes

- Compared BB ThreadTableOfContents constants and layout: minimum owning pane width 896px, minimum three user messages, maximum 20 rail ticks. Rdev now follows these limits, samples both endpoints, and closes the index when the pane narrows.
- Navigation jumps call the owning transcript's scrollTo, rather than scrollIntoView which can move ancestor/sibling layouts. The active marker now updates on coalesced scroll frames rather than relying on stale IntersectionObserver entry positions. Ordered row lookup uses binary search to avoid measuring the full transcript each frame.
- Component regressions cover owning-pane jumps, latest-message inclusion with 60 messages, the 20-tick bound, narrow-pane hiding and closed state on widening. 135 Web tests, TypeScript and production build passed. The earlier mobile navigation screenshot predates this BB width alignment; the rail is now intentionally absent in narrow panes.

## Follow-up: font verification and shortened shell results

- Inspected actual code-block computed styles: BB mono stack is applied; mobile code is 14px/20px as specified by the shared coarse-pointer typography tokens. Source font-sans/font-mono/font-terminal declarations match the BB reference.
- Found that generic head/tail truncation could remove the stdout/stderr boundary and old-history head-only compaction always removed closing tags. Shell-shaped results now shorten each stream independently and retain the status/envelope. Tests cover noisy stdout alone, noisy stdout plus stderr, UTF-8, size budgets and repeated compaction.
- Legacy head-compacted stdout results render retained text and the explicit truncation marker without exposing wrapper tags. Arbitrary malformed output retains the generic fallback.
- 137 Web tests, TypeScript and production build passed; full Go tests and Agent race tests passed. Legacy tests cover both stdout-only and partial-stderr compacted history.

## Follow-up: session management browser acceptance

Local fixture only (`@server` on port 19440); no real user history was deleted.

- 1440×1000 dark: native New Conversation created a second server thread. Native header menu → Rename opened a focused text field; Enter saved `session-management-local-review` and updated the header/sidebar.
- 390×844 touch/light: native conversation menu → Delete opened an alert dialog naming that exact thread and explaining cancellation of its work. Initial focus was Cancel. Cancel preserved the thread.
- Reopened the confirmation and deleted the test thread. The app returned to `BB interaction review`, retained `@server`, and kept the composer enabled. Reload confirmed the deleted name remained absent, the original transcript remained, the dialog was gone, and root width equaled 390px.
- Alert-dialog styles were compared directly with BB's packages/shared-ui implementation and match. Initial entry animation positions are transitional; the settled dialog fits the viewport. This exercise covers idle-thread UI management; running-work deletion isolation is covered separately by TestDeleteRunningConversationCancelsWorkAndCannotResurrectHistory.

## Follow-up: approval reconnect and viewport matrix

- Initial matrix exposed automatic rejection when the final browser disconnected (including reload/device emulation). Removed disconnect-driven denial; requests remain bounded by their existing five-minute deadline and turn cancellation. Actual WebSocket regression verifies detachment leaves approval pending, reattach replays the same ID, and explicit denial resolves without execution.
- Mobile replay exposed scroll measurement before React committed restored rows. Scroll scheduling now starts in a layout effect, retains forced jumps, and still coalesces requests per animation frame. Regression verifies no frame is scheduled before commit and manual scroll-up is preserved for non-forced updates.
- On the rebuilt local service, a pending `/bg` approval survived native reload. At 390×900, all three buttons appeared above the composer (bottom around 653px; transcript scrolled 313px of 989px content into a 676px viewport).
- Pending approval checked at 390/768/1440 × light/dark: all three enabled actions remained available, with no root horizontal overflow. One dark-mobile transitional sample preceded final positioning; after layout settled, buttons were contained. Native Deny then removed pending actions and returned the composer to its normal enabled state without executing the command.
- 137 Web tests, TypeScript, production build and full server race suite passed.

## Follow-up: split composer and empty conversation recovery

- Native sidebar-thread drag onto the conversation edge created two panes. Typing left/right drafts and using Tab to complete `/bg` affected only the focused composer. Native Shift+Enter inserted a newline. Synthetic browser composition/Enter events did not send; this does not replace a real operating-system IME test.
- Resize/reload exposed component-only draft storage. Text now uses a tab-scoped external store keyed by device/thread, synchronizes matching consumers, and restores from sessionStorage; successful clearing removes the saved value. Tests simulate module reload and same-ID conversations on different devices. Attachments are not persisted; text beyond 64K characters stays in memory only.
- Server restart exposed missing explicit empty threads, allowing two restored panes to resolve to one conversation. New empty threads now persist under their thread lock before responding. Reconciliation removes duplicate/absent pinned panes. Tests cover restart/current selection, duplicate resolution and stable reconciliation.
- Rebuilt browser verification: distinct panes held `left-refresh-draft` and `右侧草稿`; native reload restored both values under their distinct original titles. Mobile root had no horizontal overflow; side-by-side narrow-pane usability remains a separate review item.
- Removed the remaining cumulative token row from conversation Info, its formatter and the per-transcript total scan. Native Info-tab inspection shows device/project/branch/model/thread ID and no token count.
- 139 Web tests, TypeScript and production build passed; targeted empty-thread/reconnect/deletion race tests and the full server race suite passed.

## Follow-up: compact split conversations

- Narrow layouts use conversation tabs and one full-width pane, retaining the same mounted pane tree. Hidden panes are inert; separators are hidden. Wide layouts restore the original split fractions. Only the visible narrow pane is considered read.
- Component regression verifies tab ARIA linkage, native keyboard-event selection/focus, hidden/inert states, retained local state without remounts, and restored wide split fractions.
- Rebuilt local browser at 390×844: root scroll width 390px, visible pane 342px and composer 308px (previous side-by-side composers were around 136px). Both distinct text drafts remain present; hidden pane has zero rendered width and is inert.
- 140 Web tests, TypeScript, production frontend build and embedded server build passed.
- Native mobile tab click after closing the existing Info panel selected the left pane at 342px, retained `left-refresh-draft`, and hid the right pane while retaining `右侧草稿`. At 1440px both panes were visible with equal original fractions and both drafts preserved. Screenshot capture stalled, so this follow-up uses DOM geometry/state and native interaction evidence, not a new visual screenshot sign-off.

## Follow-up: file transfer rendering and persistence

- File binary frames previously serialized the full task list and triggered a file-pane render for each chunk/ack. Progress now stays in mutable task state and is sampled/persisted every 500ms; upload acknowledgments still resolve immediately. Completion, failure, pause/cancel and disconnect retain immediate persistence. Pagehide/unmount flush pending progress. Idle panes no longer render on each timer tick.
- Actual FilesBrowser regression uses two devices and 200 interleaved binary frames: zero per-frame storage writes/React commits, one sampling write with both exact offsets, pagehide flush with an extra byte, immediate failure persistence and no writes/commits for five idle seconds. Existing reconnect/auth/offset-isolation tests still pass.
- Rebuilt local service, native mobile path navigation and download of a generated 2MiB fixture: persisted completed/saved state with done=size=2097152. Task panel displays completed/100%; screenshot inspected at 390×844 light, no horizontal overflow (root 390px/table 342px).
- 141 Web tests, TypeScript, frontend production build and embedded server build passed. These checks demonstrate functional batching and completion; they are not a throughput benchmark.

## Follow-up: file task action boundaries

- Task UI and handler now share state-based action eligibility. Queued requests expose cancel, not resume/clear; completed/canceled tasks expose only clear. The handler rechecks live task identity/state so stale controls cannot apply an invalid transition.
- Extended FilesBrowser regression starts a queued upload, checks its sole cancel action, cancels, delivers late upload_ready, verifies it stays canceled, then clears it. Existing reconnect/authentication and upload-resumption tests remain green.
- Rebuilt mobile browser, restored completed 2MiB download: task has exactly one enabled button, Clear, with root width 390px. 141 Web tests, TypeScript and both frontend/server builds passed.

## Follow-up: terminal compact panes

- Real terminal review reproduced two 170.5px panes at 390px, with tab/toolbar controls squeezed. Terminal now uses the same compact tabbed SplitContainer as Agent, keeping its existing xterm instances and WebSocket connections mounted. Labels include pane number and device to distinguish multiple shells on one device. Selected compact tabs scroll into view for both pages.
- Native input set different shell variables in two panes and launched delayed output in the right pane. Switched left while output arrived, then restored wide layout: delayed output appeared on the right, both panes retained history. Resized back to mobile and `echo PANE=$RDEV_PANE` returned `PANE=right`; performance.timeOrigin stayed unchanged throughout, proving this was resize rather than page reload. Closing the right pane preserved the left history.
- Inspected light/dark mobile and dark wide screenshots. Mobile visible pane is 342px (root 390px without overflow); wide restores two equal 539.5px panes alongside the device sidebar. Rebuilt selected-tab scrolling moves the tab strip so the second label is readable.
- 141 Web tests, including shared compact-pane mount/state/keyboard regression, TypeScript and frontend/server builds passed. This verifies real Linux shell behavior through the isolated review client; it does not claim Windows/macOS terminal coverage.

## Follow-up: OMP tool-image provider conversion

- Compared OMP 75e7801 `packages/ai/src/providers/openai-completions.ts` toolResult conversion: text tool replies precede one user image attachment batch. Rdev read previews previously reached the UI but were plain base64 text in provider requests. Added request-boundary conversion of complete bounded JPEG preview envelopes to the same ordering, preserving tool_call_id and leaving persisted/UI messages untouched.
- Wire tests cover multiple images mixed with text-only results, trailing result groups, intact source history, malformed/oversized/truncated previews and user text that resembles the preview envelope. A real two-round httptest HTTP exchange runs a read-preview fixture tool and checks the next provider request contains image_url while the tool_end UI event retains the original preview.
- Agent race tests passed. These are protocol/transport checks; the fixture response is not evidence of an actual model's vision quality. Existing bounded preview resolution and model vision capability remain practical limits.
- Full `go test ./...` and server build also passed after the provider conversion.

## Follow-up: deleted conversation drafts

- The matching successful thread_deleted response now clears the device/thread text draft from the shared live store and tab sessionStorage. Canceled or failed delete attempts do not clear it. This cleanup applies to the requesting browser; other browser tabs retain their own tab-scoped storage until separately reconciled.
- Hook regression verifies mirrored consumers clear, another device with the same thread ID retains its draft, and module reload does not resurrect the deleted draft.
- Native browser created a disposable empty @server conversation and typed a draft. Canceling the delete dialog retained the exact saved value; reopening and confirming deletion removed its storage key and cleared the visible composer.
- 141 Web tests, TypeScript, frontend build and embedded server build passed.
