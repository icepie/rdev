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

No production deployment occurred. The original full-resolution sent image is not persisted; the bounded preview is. Older history cannot recover absent timestamps or image data. Windows non-PTY process-tree cancellation now uses Job Objects and has isolated Wine coverage (see follow-up below), but Windows hardware is not verified; Unix process-group tests ran on Linux, not macOS hardware. The canvas is an isolated retained call preview, not Hope's complete artifact/version/export platform. Hardware GPU capture and the complete active-state animation/overlay matrix remain separate acceptance work.

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

- The matching successful thread_deleted response now clears the device/thread text draft from the shared live store and tab sessionStorage. Canceled or failed delete attempts do not clear it. The initial cleanup covered the requesting browser; the follow-up below extends it to other connected browsers for the same device.
- Hook regression verifies mirrored consumers clear, another device with the same thread ID retains its draft, and module reload does not resurrect the deleted draft.
- Native browser created a disposable empty @server conversation and typed a draft. Canceling the delete dialog retained the exact saved value; reopening and confirming deletion removed its storage key and cleared the visible composer.
- 141 Web tests, TypeScript, frontend build and embedded server build passed.

## Follow-up: cross-browser deletion notification

- Successful deletion now emits a device/thread-tagged notification to every browser attached to that device, in addition to the requester's correlated acknowledgment. The UI discards matching text drafts on either confirmed event, while pending dialog/error state still uses request correlation.
- Extended the actual WebSocket deletion race regression to two browsers. It checks the requester receives its request ID, the peer receives an uncorrelated notification with the exact device/thread, both are switched away from the deleted thread, active work is canceled, and late completion cannot resurrect persisted history.
- Targeted server race tests, 141 Web tests, TypeScript and frontend/server builds passed. Browsers disconnected throughout deletion do not receive this live notification; their tab-scoped stale drafts are not claimed to be pruned by this change.
- Full server race suite also passed after the deletion broadcast change.

## Follow-up: Windows command-tree cancellation

- Each non-PTY Windows execution now owns an unnamed Job Object with kill-on-close. The shell starts suspended, is assigned to the job, then resumes; cancellation is serialized with setup so an early cancellation cannot terminate an empty job and miss the process. Cancel terminates descendants; after Wait, closing the job also removes surviving descendants. Startup failure kills/reaps the suspended process and releases the job handle.
- Initial Windows runtime test exposed Go argument quoting breaking a quoted executable path through cmd.exe. For cmd.exe /c, preserve the raw command tail via SysProcAttr.CmdLine; other executable argument handling remains standard.
- Built a Windows amd64 test executable and ran it under Wine 11.21 in a new isolated /tmp prefix on Xvfb :98. Tests start a shell -> helper -> grandchild tree using an executable path containing spaces, cancel it, wait for the grandchild process handle to signal exit, and verify an independent concurrent session completes. Repeated close, canceled-before-start, cancellation during startup, and missing executable cases pass.
- Linux client race tests and full Go suite passed. Windows amd64 and arm64 client binaries cross-build successfully. Wine validates these Windows API paths in compatibility emulation; native Windows job nesting/service-host policies and Windows/macOS hardware remain unverified. No production deployment.

## Follow-up: canceled tool batches

- The agent loop now checks the context before tool lookup/approval and again after approval returns, before Execute. A tool remaining in a canceled batch gets an explicit canceled result without invoking its implementation; the complete assistant tool-call/result chain is still retained.
- A multi-tool model-response regression cancels after the first read tool, verifies the following write tool (which intentionally ignores context) never executes, checks exactly one provider request, and checks paired history and UI end events. Separate cases cover an already canceled turn and approval returning allow after cancellation.
- Agent race tests passed. The guard covers cancellation already observed at the execution boundary; an operation already inside Execute still relies on its own cancellation implementation.
- Full Go suite and server build passed after this change.

## Follow-up: measured output retention hotspot

- Added reproducible backend job-output and client framing benchmarks; results and limits are in `2026-11-08-output-performance.md`.
- Replaced per-delta tail-string concatenation with bounded ring-buffer storage. Reads/finalization produce immutable snapshots; finalization releases the live buffer. Truncated result/error strings are cloned so they do not retain arbitrarily large original allocations.
- At 32B deltas over 256KiB with periodic snapshots, three-run median cumulative allocation fell from 311,276,068 to 1,159,810 B/op and time from 29,164,617 to 137,614 ns/op. This is a local retention-path microbenchmark, not end-to-end throughput.
- UTF-8/chunk-boundary and snapshot regression, full server/client race suites and server build passed.

## Follow-up: drafts after missed offline deletion

- Confirmed agentSessionFor hydrates all persisted device threads and agentThreadListLocked returns the entire unpaginated list. Text drafts now reconcile only on auth_ok using that full list, before adopting its thread state; live/filtered sidebar snapshots are not used for pruning.
- Reconciliation requires a valid device/current thread, a well-formed thread array and presence of the current thread. Missing/malformed legacy responses cannot delete drafts. It checks both live cache and sessionStorage keys, retains other devices, existing threads, unassigned drafts and unrelated/malformed storage keys.
- Hook regression checks mounted stale draft clearing, storage-only stale entries, device isolation and malformed/missing snapshot cases. Rebuilt browser seeded a missing-thread draft in @server and a same-name draft on another fixture device, then performed native reload/authentication: missing-thread key was removed, other-device value and the current typed draft remained intact. This browser check uses seeded stale storage, not a simulated network outage.
- 142 Web tests, TypeScript, frontend and embedded server builds passed. Reconciliation occurs when that device authenticates again; merely remaining offline cannot trigger cleanup.

## Canvas interactive state preservation

- HTML canvas source/preview toggles now hide and inert the existing iframe; theme/preset changes send presentation-only updates instead of replacing srcDoc. A content revision deliberately creates a fresh document.
- The iframe listener accepts only parent messages with the exact theme type, a light/dark mode and CSS string. Opaque-origin sandbox remains `allow-scripts`; existing network/form CSP restrictions remain.
- Regression coverage checks frame identity, immutable srcDoc, hidden/inert behavior, content revision replacement, rejected messages and retained input state. All 144 Web tests, TypeScript, frontend build and embedded Go server build passed.
- Native Chrome verification used an isolated temporary Vite harness importing the actual CanvasPreview and ThemeProvider, not a model response or full conversation round trip. Typed `keep canvas input`, incremented counter to 1, switched Source/Preview and Nord preset, then dark to light: input and counter survived, the iframe identity stayed unchanged, and its own theme observer reported light. The sandbox attribute remained `allow-scripts`. Temporary harness files were removed.

## OMP steering at settled boundaries

- Reference: OMP `75e7801`, `packages/agent/src/types.ts` getSteeringMessages contract and `agent-loop.ts` response/tool-batch boundaries. rdev now accepts one queued steering message per settled boundary, without canceling the current response or any emitted tool call. Multiple steering messages are FIFO ahead of normal follow-ups. Slash commands retain separate command dispatch.
- Input is not consumed after cancellation, epoch replacement/deletion, or the last allowed round. Late arrivals remain in the turn queue. Existing stop clears the queue. The new thread-scoped `steering` frame creates a user bubble, finishes prior streaming indicators and preserves the running clock; display metadata survives history without entering provider input.
- Go regression coverage includes an in-flight provider request with two steering messages plus a normal follow-up, paired results for a two-tool batch before injected input, retained queue ownership on cancel/round limit, and rejected late steering events after reset. Frontend regression verifies transcript separation, cleared text/reasoning cursors and stable elapsed-time origin.
- Validation: 145 Web tests, TypeScript, frontend/server builds, full Go suite and Agent/server race suites passed.
- Native Chrome with an isolated local server and gated SSE fixture: sent a prompt, clicked 追加指导 while the first response remained blocked, confirmed it stayed queued while the original response continued, released the response and observed the new instruction answered afterward. History/Steer metadata survived reload and server restart. A second run on the final build confirmed no stale streaming cursor after the boundary. This verifies lifecycle and UI, not real-model task quality.
- Scope: boundary-based steering, not provider-native injection into an open stream, interruptible waiting-tool orchestration, or complete OMP RPC compatibility.

## Guidance interrupts passive job waits

- Reference: OMP `75e7801`, `packages/coding-agent/src/tools/wait.ts` interruptible wait and the Agent queue-peek contract. Only rdev `jobs wait` yields early when user guidance is queued; shell execution, background job contexts and other threads are unaffected.
- A per-thread notification channel wakes active waiters. Checking pending steering also handles guidance admitted before a wait starts. The observer never consumes queue entries; removed guidance does not leave a closed signal for a future wait. No polling timers or watcher goroutines are added.
- Early wait results preserve the latest bounded output and add response-local `waitInterrupted: true`. The flag is not persisted into job state. Normal completion, timeout, cancellation and retention continue using the captured job identity.
- Regressions exercise guidance before/during a real jobsTool wait, queue ownership/removal, separate-thread isolation, completion after interrupted wait, and absence of stale markers. A full server/session HTTP-provider test emits an actual jobs wait call, admits steering after the waiter registers, and verifies the next model request contains the interrupted tool result followed by the new user input while the background task remains running.
- Validation: focused steering/job tests under the race detector, complete server race suite, full Go suite, and embedded server build passed. No frontend changes in this follow-up; browser steering rendering was verified in the preceding section.

## Stable queue removal and acknowledged text return

- Fixed an index race: worker admission or steering could shift rows between rendering and clicking, causing dequeue to delete a different message. Queue IDs now survive reordering and are carried in auth/switch/peek snapshots and live queue broadcasts. Identity-based removal never falls back to an index.
- Take-back editing now waits for a correlated requester-only `dequeued` success before adding original text to the composer. Failed sends, stale removals and duplicate replies leave the draft intact; switching threads clears pending returns. Legacy index callers remain supported but do not gain the new concurrency guarantee.
- A real WebSocket test seeds identical text rows, advances the queue before a browser request, and checks stale, cross-thread and duplicate removals plus request-ID acknowledgements. Frontend tests cover delayed success, duplicate/stale acknowledgement, failed send, missing identity and thread-switch cleanup.
- Native Chrome on the rebuilt isolated service: queued `return-this-message` during a gated response, took it back through the row button, confirmed restored composer text, queued it again, removed it with the X control, released the response and confirmed the removed text was neither executed nor restored.
- Validation: 147 Web tests, TypeScript, frontend/server builds, focused WebSocket queue race tests, complete server race suite and full Go suite passed.

## Queued image take-back and reference release

- Restoring queued text previously discarded attached originals. A requester-only successful `dequeued` response now returns originals when `restore: true`; normal removal and all queue snapshots omit original data. The composer reconstructs previews, keeps original bytes for resending, and blocks image admission/send during the short return request. Existing image drafts prevent take-back until sent/removed, with an explanatory hint. Image-only queue rows now have a visible label.
- Queue removal/admission shares a helper that zeroes the vacated backing-array slot. Tests inspect the former backing slots to verify removed attachment references are released, including the final item.
- The WebSocket regression verifies returned originals and their absence from auth snapshots/plain removals. The composer regression verifies preview restoration, blocked concurrent image reads, no overwrite of an image draft and exact attachment bytes in the subsequent chat send.
- Chrome on the rebuilt isolated service confirmed a queued PNG returned to the composer with the same data URL, original name and a loaded preview (`naturalWidth > 0`). The file-upload tool rejected both temporary and workspace paths, so the PNG was created in page memory and supplied through a synthetic file-input change; actual queue/take-back used native button clicks. This does not verify an OS file chooser.
- The first full Go run exposed an existing concurrent-chat test assumption: waiting for `turn_start` discarded a valid earlier `queue` frame from the competing connection. The test now collects both in either order, still requires exactly one initial start and one queued item, and releases its gated provider on failure so cleanup cannot hang.
- Validation: 147 Web tests, TypeScript, final frontend/server builds and full Go suite passed. Complete server race suite passed before the constant-time head-pop refinement; focused queue/admission tests passed under race detection afterward. The corrected concurrent-chat test passed 20 consecutive runs under race detection.

## BB image lightbox alignment

- Direct reference/copy: BB `f51e6b0` `apps/app/src/components/ui/image-lightbox.tsx`, `pan-zoom.ts`, and shared `lib/image-preview.ts`. rdev adapts local imports and translations, omits the unused video path, and wires message/composer image groups to wrapped navigation. read_file keeps the same shared single-image entry point.
- Replaced the generic dialog/actual-size toggle with the BB fullscreen black/70 overlay, 82dvh fitted image, themed transparency checker, 44px controls, safe-area positioning, pan/zoom limits and reduced-motion behavior. The existing BB-derived overlay focus manager restores focus and contains keyboard navigation. Only an open lightbox installs pan/zoom listeners.
- Regression tests cover zoom normalization, limits, focal point, pinch geometry, constrained pan, gallery wrapping, modified arrow keys, click/escape dismissal, zoomed-image click, fit/reset, focus restoration and wheel listener removal after close. All 159 Web tests, TypeScript, frontend build and embedded server build passed.
- Native Chrome: supplied two generated in-memory PNGs through file-input change. At 390x844 light/reduced motion, native thumbnail click opened an exact 390x844 dialog with black/70 background, no root overflow and animation none; screenshot inspected transparency checker and safe-area controls. Native ArrowRight switched to image 2 and Escape restored focus to image 1 thumbnail.
- At 1440x1000 dark/normal motion, the overlay covered the viewport and used enter animation. Synthetic wheel events zoomed to 3.2973x, native drag changed pan to (598.922,-324.326), native fit reset to (0,0)/1x, Tab remained in the dialog, and Escape closed. These are desktop-browser/emulated-mobile checks; real touch hardware and OS file chooser were not exercised.

## Files compact pane layout

- Files previously stacked two browsers vertically below 900px, reducing each listing/task workspace to half height. The extracted FilesPaneLayout now shows one full-height tab panel in compact workspaces and both panels side by side when the content area is wide enough. Measurement uses actual workspace width, including device-sidebar effects.
- Both browser components remain mounted. Hidden panes are inert, tab controls have linked panel IDs and roving keyboard focus, and an internal file drag entering a compact tab selects that destination without resetting either browser. Closing/reopening the second pane preserves its state.
- Regression exercises container resize, ArrowRight navigation, hidden/inert state, close/reopen, stored pane state, drag-enter selection and observer cleanup; the existing full Files reconnect/transfer suite still passes. Final validation: 160 Web tests, TypeScript, frontend build and embedded server build passed.
- Actual local devices `desktop-review` and `work-review`: navigated to `/tmp/rdev-file-ui-fixture` and its empty-folder in separate panes. At 390x844, the visible panel measured 342x787 and the other was hidden/inert; tab changes preserved both paths. Resizing the same document to 1440x1000 restored two 540x951 panels in a 1080px workspace after ResizeObserver delivery, with original panel nodes still connected and unchanged performance.timeOrigin. Root overflow was absent in compact layout. Native sidebar device selection and ArrowRight tab selection worked.
- The optional screenshot request did not return and was terminated after repeated waits; no screenshot result is claimed. Layout evidence here comes from DOM geometry/state and actual UI operations. Native cross-tab file dragging and a long-running transfer across a breakpoint were not exercised in this browser run; mount/state and existing transfer regressions cover their respective boundaries.

## Failed stream history and provider finish reasons

- Partial text/reasoning now remains in history after EOF, upstream errors and output limits as well as cancellation. Pending tool-call fragments are removed before persisting the partial assistant message; failed streams never execute them or emit successful completion.
- Compared directly with OMP 75e7801 openai-completions mapStopReason: completion aliases are case-insensitive, MAX_TOKENS maps to truncation, and content_filter/network_error/error/unknown finish reasons fail rather than allowing incomplete tool execution. This aligns finish classification, not OMP's full retry policy.
- HTTP regressions cover cancellation, EOF, length, content_filter and inline provider errors with partial reasoning/text and pending tool arguments. They verify preserved history, no tool execution or Done event, no automatic replay after output, and a successful next provider request without dangling calls. A separate classification matrix covers success aliases and failure reasons.
- Agent race tests, the full Go suite and the embedded server build passed. No frontend assets changed.

## Live reasoning event transport

- Found a producer/consumer mismatch: Session emits EventReasoning chunks in Text (the documented event contract), while the server read Reasoning, which is reserved for the final EventDone aggregate. Live thinking frames and reconnect replay therefore contained empty strings even though final history retained the thinking text.
- The server now forwards Text for reasoning events. The stale-event test fixture uses the actual producer contract.
- A real HTTP SSE → Agent Session → WebSocket regression gates provider completion until two Chinese reasoning chunks have arrived, reconnects a browser socket, verifies complete live replay before releasing the provider, then checks answer delivery and the final reasoning aggregate. It passed three consecutive runs under race detection.
- The frontend thread-sink regression verifies reasoning becomes visible before Done, accumulates successive chunks, and finalizes without duplication. All 11 focused thread-sink tests, the full server race suite and the embedded server build passed. This is automated transport/state verification, not a new browser visual inspection.

## Tool schema validation before execution

- Inspection found write_file advertised required content but its Go decoder treated missing/null content as an empty string. Session previously checked JSON syntax only, so a malformed model call could become an unintended empty-file write.
- Added a shared JSON Schema validator (santhosh-tekuri/jsonschema v6.0.2), cached by schema within its owning session/router. Direct model calls are validated before approval and execution. Server-mode device_tool also validates the selected inner tool's schema before forwarding execution; the outer device_tool approval can still occur before inner validation.
- Matches OMP's schema-validation boundary and paired-error recovery: invalid calls produce ordinary failed tool results and the next model round can correct them. Explicit content:"" remains valid. No automatic coercion/default injection/OMP argument healing is claimed; validation does not rewrite commands, paths or content.
- Self-contained schema references are supported; external network/file references are disabled. Tests cover required fields, null/type errors, closed objects, nested arrays, enum/integer/range constraints, local references, broken schemas and external references.
- HTTP model-loop regression verifies an invalid write call skips approval/execution, retains its paired error, and a corrected explicit empty write succeeds on the next round. Actual write_file/bg/jobs/subagent schemas are exercised through HTTP provider calls. A server-mode routing regression checks invalid nested write/shell calls emit zero device messages.
- Agent race and full server race suites passed before extracting the validator for nested routing; focused Agent/server schema regressions passed under race detection after extraction. Final full Go suite and embedded server build passed.

## Compact live replay for long outputs

- Replaced per-delta retained JSON with a thread-owned replay buffer. Only adjacent text/reasoning deltas smaller than 256 bytes are coalesced, up to 8KiB per block. Larger frames and all lifecycle/tool/approval/steering frames preserve their original payload and order. Live broadcasts remain unbatched.
- Snapshot and reset regressions verify complete Unicode/escaped content, immutable old snapshots, thread and lifecycle boundaries, and no prior-turn text after reset. Existing actual SSE/WebSocket reasoning reconnect coverage also exercises the new buffer.
- Benchmark and limits are recorded in output-performance.md: 256KiB delivered in 32B deltas changes replay from 8192 frames/606208 JSON bytes to 32 frames/263488 bytes. This measures replay storage, not RSS or end-to-end reconnect latency.
- Validation: complete server race suite passed before the large-delta bypass refinement; final focused replay/reconnect/stop/approval race tests, full Go suite and embedded server build passed after refinement. No browser latency or visual test was added in this change.

## Preserve bottom following across tall streamed blocks

- Compared with BB f51e6b0 BottomAnchoredScrollBody/ScrollPositionKeeper's explicit bottom attachment: rdev previously tested proximity only after React committed new content. A block growing by more than 120px could therefore detach a reader who had been at the bottom.
- The sink now captures attachment once before the commit and uses the new DOM only for the eventual scroll destination. Pending requests remain coalesced into one animation frame. Moving upward before that frame cancels automatic following; replacing the transcript element clears the old attachment. Explicit forced jumps retain their existing semantics.
- Regression covers a tall block, multiple updates before paint, manual upward scroll during a pending frame, continued output while reading above, replacement panes and existing forced-jump coalescing. All 164 Web tests, TypeScript, frontend build and embedded server build passed.
- Actual Chrome component harness using the production hook: 300px viewport at scrollTop 900/scrollHeight 1200 followed a 2000px addition to scrollTop 2900/scrollHeight 3200 (bottom gap 0). After setting scrollTop 200, another 2000px addition left scrollTop 200. Actions were script-driven against real layout; this was not a native wheel/touch test or a full page visual review. Temporary harness and Vite process were removed/stopped afterward.
- This fixes streaming commit attachment. It does not claim BB's full row-anchor persistence or asynchronous image-resize reconciliation.

## BB working sweep and back-to-bottom semantics

- Direct reference: BB f51e6b0 FollowUpPromptBox passes runtime active state to ThreadTimelineScrollToBottomButton, whose icon uses animate-shine-icon. rdev instead tinted the icon as unread and set unread immediately on upward scrolling. It now follows BB's running-state semantics, resetting bottom visibility on device/thread changes.
- Copied the original 2593-byte, 128×2, 30-frame APNG mask and corresponding text/icon CSS, with source attribution. Working text and the thinking header share animate-shine; the reasoning body no longer pulses. Hidden/inert and reduced-motion rules remove the mask and release the promoted layer. This is the referenced working cue, not a claim that every application animation matches BB.
- Actual Chrome component harness: light normal-motion labels/icons used the APNG mask and transform promotion; inert/aria-hidden hosts used mask none and will-change auto. Dark normal-motion at 390px used the dark foreground (oklch 0.81) without root overflow. Dark reduced-motion disabled both masks/layers. Toggling running false removed the visible working classes. These are computed-style/state checks, not frame-by-frame animation timing measurements or a full page visual review. Temporary harness and Vite process were removed/stopped.
- All 164 Web tests and TypeScript passed. Frontend and embedded server builds passed; production CSS was checked to contain the exact BB APNG data, and the copied asset is byte-identical to the reference.

## Separate reasoning, text and tool activity

- Non-empty answer deltas now stop the thinking indicator; reasoning deltas stop the text cursor; tool_start closes both streaming indicators. A later reasoning round can reopen thinking. Empty fragments neither create blank turns nor change the current phase.
- TurnView receives explicit whole-turn activity from the current open turn, instead of using the text-streaming flag as a proxy. Tool phases keep live rows expanded and omit the duplicate ask tool row while the interactive question card is active. Settled actions appear only after the turn closes.
- State regression covers reasoning → text → tool → reasoning → tool → text → done, preserving overall running/tool state. Rendering regression covers active tool grouping, duplicate ask suppression, absent text cursor and settled actions. All 167 Web tests, TypeScript, frontend build and embedded server build passed. No additional browser visual run was performed for this state change.

## Reset releases admission without stale-turn cleanup

- Reset previously left running/cancel/context attached to the canceled turn until its goroutine unwound. If reset happened before admission, the old goroutine returned early without clearing running, leaving the thread busy. Reset now increments the generation and clears foreground ownership immediately, while canceling the captured old context.
- Normal completion checks generation, thread identity and context ownership before changing running state, approvals or queue. Panic recovery applies the same ownership check, so a late old tool failure cannot clear a replacement turn or publish its error against that turn.
- A deterministic gated tool deliberately ignores cancellation until released. Tests reset its thread, admit a new gated turn, install a pending approval and queue entry, then release the old tool normally or with a panic. The replacement remains running with its own context/approval/queue intact and completes with only its own persisted history. The reset-before-admission regression now also asserts the thread is not left busy.
- Focused new ownership regressions passed 10 consecutive runs under race detection; reset/WebSocket late-event regressions passed three runs earlier. Full Go suite, full server race suite and embedded server build passed.

## Unexpected execution failures settle through the normal lifecycle

- The outer panic fallback previously released running state but bypassed persistence and queue admission. Execution now converts tool panics into failed, paired tool results; unexpected conversation/provider failures return an error through the ordinary finalizer so queued work proceeds. The outer fallback remains for unexpected lifecycle/plumbing failures.
- A compaction that already replaced history records that fact before emitting UI events, preserving replacement persistence even if event delivery unexpectedly fails.
- Model-loop regression verifies a panicking tool emits ToolEnd with IsError, preserves its call/result pair and lets the model respond to the failure. Queue regressions simulate a failing explicit tool command and a panicking provider RoundTripper; each admits and persists the queued command afterward. Focused regressions, including reset ownership, passed five consecutive race runs.
- Final validation: full Go suite, complete Agent/server race suites and embedded server build passed.

## Terminal frames settle unfinished tool cards

- A terminal error/stopped frame previously closed the assistant text flags but left tools without tool_end marked running. The completion reducer now settles those cards with the terminal reason, keeps any partial output, and leaves already-completed results/timestamps unchanged. A successful turn with a missing tool result uses an explicit localized result-unavailable message instead of implying success.
- Completion arguments now use a named options object; the done path reuses parsed usage instead of duplicating its field validation. Browser disconnect still leaves unknown remote tool outcomes unresolved for reattach, because disconnection is not proof the server stopped execution.
- Parameterized regressions cover error, stopped and done-with-missing-result, completed-result preservation, partial output, done timing and open-tool cleanup; a separate regression preserves unknown outcomes on disconnect. All 171 Web tests, TypeScript, frontend and embedded server builds passed. No new browser visual check was performed.

## Live-page phase review and settled question deduplication

- Started an isolated latest-build server on loopback 19640 with a scripted SSE provider on 5309. Native browser input sent a prompt; the provider held a reasoning phase until released, then emitted an ask call and a final TypeScript answer after native option selection/submission.
- At 390×844 light/normal-motion, reasoning and working labels used the production embedded sweep, with no text cursor or horizontal overflow. Entering ask stopped reasoning/text streaming and showed one question card. The choice measured 280×53.25px and stayed inside the content viewport; submit measured 52×40px. Dark/reduced-motion switched mask to none and layer to auto. The actual dark question screenshot was visually inspected.
- The completed page exposed duplicate question representations: the settled ask tool row plus the answered live card. Ask cards now retain the owning turn key and pending tool ID. A pure projection suppresses an answered card only when its corresponding completed tool row is visible; active, pending, unlinked or unmatched cards remain available. Turn identity prevents reused tool IDs from hiding another turn's card.
- After rebuilding/restarting the isolated service, a fresh native browser conversation showed one interactive question during execution. Resizing the same document from 1440px light to 390px dark/reduced motion preserved it. After answering, exactly one settled question row remained, with no duplicate answered card, active sweep or text cursor. Expanding that row showed the original question and chosen answer. Mobile dark and desktop light screenshots were inspected; both had no horizontal root overflow, and code tokens used their respective BB light/dark color ramps.
- Screenshot file-path saving was rejected by the browser tool for both temporary and workspace paths; screenshot images were instead returned directly and visually inspected. No saved screenshot artifact is claimed. This adds real running/interactive evidence, but does not complete every route/overlay/platform matrix.
- All 173 Web tests, TypeScript, frontend and embedded server builds passed. Unit coverage verifies request-ID/tool-ID association, active versus settled visibility, reused tool IDs across turns, and preservation of unresolved/unlinked cards.

## Transcript attachment across asynchronous layout changes

- Compared BB f51e6b0 `bottom-anchored-scroll-body.tsx` and `scroll-position-keeper.ts`: BB observes both the scroll viewport and content. Rdev now applies that observation to each conversation pane, complementing the existing pre-commit stream anchor. Delayed images, disclosure layout and viewport changes reconcile against prior geometry; scroll-up beyond the bottom threshold releases attachment. Native clamping after a collapse is handled separately from an upward reader movement.
- The back-to-bottom control uses the same observer's attachment state, so layout changes update it even without a new protocol frame. Observer/listener disposal prevents an old pane's queued callbacks from affecting its replacement. The existing 120px threshold is shared with the stream sink.
- Six regressions cover content/viewport resizing, detached reading and reattachment, resize-before-scroll and scroll-before-resize ordering, collapse clamping, split-pane isolation, cleanup and hidden-pane observation. All 179 Web tests, TypeScript and frontend build passed.
- Real Chrome used a temporary Vite fixture importing the production observer with real DOM/ResizeObserver (synthetic content, not a live agent conversation). A 300px viewport with content growing 1200→3200px followed from scrollTop 900→2900; the second pane remained at 200. Shrinking the viewport to 200px followed to 3000. After scrolling up to 400, further content growth preserved 400. A delayed synthetic SVG image then grew the reattached content to 4600px; scrollTop 4415 equaled the actual bottom with a 185px client height. The fixture was removed afterward.
- This is bottom-attachment reconciliation, not BB's complete saved-row anchor restoration or iOS momentum-scrolling implementation. No mobile hardware or full visual matrix claim is made.

## Model response boundaries match persisted history

- Found a live/history discrepancy: the live sink concatenated all assistant text before its tools, while history contains separate assistant messages per model response. Added a thread-scoped `assistant_start` event at each model round, following OMP 75e7801's explicit message boundary design. The server broadcasts and retains it in reconnect replay. The browser closes the prior message's streaming flags and lazily opens the next message when content or tools arrive, leaving the user turn and working clock running.
- The final done reasoning remains the final response's reasoning; earlier response reasoning stays with its own tools. Tool-only rounds are also distinct, including when a provider reuses a call ID. A next response failing before output does not create an empty bubble.
- Agent regression compares streamed response boundaries/content/reasoning/tool counts against persisted assistant messages. Server tests cover reset rejection, thread identity and replay retention; the real SSE→WebSocket/reconnect test now asserts the boundary arrives before reasoning both live and replayed. Browser reducer tests compare batched live frames, reconnect replay and restored history, and cover tool-only rounds/reused IDs and failure before output.
- All 182 Web tests, TypeScript, frontend build, full Go tests, full Agent/server race suites and embedded server build passed. No new visual browser review was performed for this change. This aligns message ordering; it does not implement full OMP RPC or provider protocols.

## History preserves tool failures and response-local result pairing

- Found that live `tool_end.isError` was discarded by persistence, while browser history replay hardcoded success. Session/model tools and explicit tool commands now retain IsError on call display metadata; JSONL save/load and history rendering carry it to replay. It is omitted from provider wire serialization. Legacy records without the metadata retain their old default; error-looking file content is not classified heuristically.
- Historical rendering previously built one result map for the entire conversation, so a repeated call ID showed the newest result in earlier responses. Results now come only from the contiguous tool-result messages belonging to that assistant response. Reasoning-only responses are also retained instead of being filtered out.
- Regressions verify denied model and explicit-command calls retain failure metadata, success remains success, save/load/render preserve distinct results for reused IDs, reasoning-only history survives, and browser replay honors explicit failures without guessing from text.
- All 183 Web tests, TypeScript, frontend/embedded server builds and full Go tests passed. The first race invocation collided with Vite replacing embedded static assets and failed compilation; it was rerun after the build completed and both complete Agent/server race suites passed. No new browser visual review is claimed.

## Agent file list opens existing preview tabs

- The embedded Files panel rendered file names as buttons but handled only directories, leaving file-name clicks inert despite the right panel already having BB-style file preview tabs. File names now call the existing `openFileTab` path, reusing tab deduplication, lazy file loading, Markdown/source modes and code highlighting. Directory navigation and the separate download action remain available.
- Both file and directory names receive pointer/hover/focus feedback. Icon-only download actions now expose a localized accessible name including the filename.
- All 183 existing Web tests, TypeScript, frontend and embedded server builds passed; no new behavioral unit test or browser visual review was added for this small callback wiring change. Standalone Files still downloads on filename click and does not yet provide its own preview; this does not claim complete file-surface parity.

## Standalone Files shares Agent file previews

- Extracted the existing BB-style `FilePreview` into a shared component. Standalone Files filename clicks now open a responsive preview; Markdown rendered/source modes, syntax highlighting, line wrapping, copy path, bounded text and image display use the same implementation as Agent. Standalone previews omit the Agent-only add-to-chat action. Directory clicks still navigate; separate row and preview-footer download buttons download the original file. The footer includes an explicit close action for touch users.
- Added `/files` WebSocket `read` → `file_content`, authorized through the existing device-password/admin-token boundary before execution. It reuses the existing bounded Agent probes (256KiB text / 4MiB image raw-byte cap) and replies only to the requesting socket with device/request identity. No agent provider configuration is required. Each pane rejects stale, closed, wrong-device and unrelated replies; device changes/unmount invalidate pending UI work. A 30-second UI timeout permits retry; closing the preview discards its eventual result while the existing bounded server probe may finish.
- Regressions extend the real Files WebSocket offline/locked-device correlation test to `read`; browser reply tests cover stale/closed/other-device results, text truncation, image metadata and errors. All 185 Web tests, TypeScript, frontend/embedded-server builds, full Go tests and targeted Files/read-probe race tests passed.
- Started a fresh server on loopback 19650 and a real client `preview-review` on 19651, with Agent unconfigured. Native browser input listed `/tmp/rdev-files-preview-fixture`; clicking hello.md rendered Markdown, switching to source showed code, image.png decoded at its expected 1×1 dimensions, and data.bin returned an explicit binary-file error. 1440×1000 light and 390×844 dark/reduced-motion were checked; mobile dialog stayed within the viewport with no horizontal overflow. The final mobile screenshot was visually inspected directly. Native footer download created the file-download blob, and Close returned focus to the filename. The fixture data is synthetic; no desktop capture was used.
- Complete BB CSV/video/external-open behavior, mobile hardware testing and all split-device combinations remain outside this verification; no claim of complete file-surface parity is made.

## Embedded Files uses shared request and frame validation

- Audited the Agent's embedded file manager after connecting its preview action. It still accepted directory responses without checking request identity and maintained a second unchecked binary decoder. Reused the standalone Files `createListingGuard`/`readListingPage` and transfer frame codec instead of duplicating them.
- The latest directory request owns the view; older success/error and duplicate replies are ignored. Pagination must return the requested offset and advance, preventing legacy first-page duplication. Device cleanup invalidates listing ownership, and delayed Blob decoding checks its originating socket before dispatch. Malformed/truncated frames now return without throwing or corrupting task offsets. Toolbar Up/Refresh/Upload buttons have localized accessible names.
- Three actual mounted PanelFiles component tests drive mocked WebSocket delivery: newer-before-older responses, stale errors and duplicates; invalid load-more pagination; device replacement followed by old socket delivery and a malformed binary frame. These complement the shared codec/guard tests. All 188 Web tests, TypeScript, frontend build and embedded server build passed. No new visual browser review was performed for this protocol/state correction.
- This brings directory ownership/frame decoding into parity; it does not claim the embedded transfer implementation yet shares every standalone task retry, attempt-ID or performance mechanism.

## Embedded file transfer progress sampling

- Embedded PanelFiles still wrote localStorage and requested a render on every upload acknowledgment/download chunk, plus every idle timer tick. It now updates mutable progress per frame and samples persistence at 500ms. Visible panes render once per dirty sample; hidden panes persist without progress renders. The stall watchdog continues while hidden. Lifecycle transitions still persist immediately, and device switch/unmount flush unsampled progress using the original device key.
- Late binary acknowledgments/chunks/end frames are ignored for incompatible or inactive tasks; late start responses cannot restart paused/canceled tasks. Completed browser downloads release retained byte buffers after creating the Blob.
- Mounted component tests use a controlled clock and React Profiler. A burst of 200 download frames performs zero writes/commits before sampling, then one storage write and one visible commit (zero hidden commits); subsequent idle ticks perform neither. Canceled-task late chunks remain ignored. A separate device-switch regression verifies unsampled progress is saved under the old device only. This measures update frequency, not end-to-end throughput, RSS or latency.
- All 191 Web tests, TypeScript, frontend and embedded server builds passed. No new browser visual review was performed for this scheduling change.

## Embedded uploads settle and isolate resume attempts

- Found that PanelFiles handled `transfer_end` only as download completion, leaving uploads running after server completion. Successful uploads now settle, persist the final byte count, release the source File and refresh the current directory. Errors/remote cancellation abort outstanding upload work.
- Reused the standalone `uploadChunks` cancellation/acknowledgment-timeout loop and `createTransferAttempts`. Stable UI task IDs now map to a fresh wire ID on every download/upload start or resume. Text/binary replies resolve only current wire IDs; pause/cancel/clear and completed transfers revoke ownership. Old acknowledgments, cancellation and error frames cannot modify a replacement attempt. Socket close aborts pending upload work; reconnect resumes running uploads as well as queued ones. Cleanup aborts work for the departing device.
- Added accessible names to pause/resume/cancel task actions. Mounted component tests perform file selection, upload_ready, binary acknowledgment and transfer_end, asserting completed state and directory refresh. Further tests pause/resume around an outstanding acknowledgment and reconnect after all bytes were received, checking fresh IDs and exactly one completion for the active attempt. Existing download progress tests continue passing with wire-ID routing.
- All 194 Web tests, TypeScript, frontend and embedded server builds passed. This turn used deterministic component/socket tests; it did not repeat a real remote-client upload or browser visual review.

## Embedded upload recovery preserves its destination

- Reused standalone `canResumeUpload` and `restoreFileTask` in PanelFiles. A new upload only matches a failed task when device, destination directory, filename and size all match. Reloaded queued/running uploads become failed with the file-reselection message because browser File bytes were not persisted.
- Explicit Resume with a missing File now records the intended task. The file picker validates one matching name/size using the shared resume validator, then resumes that task's original destination even if the visible directory has changed. Mismatches produce a task message and no upload request; a normal Upload action clears the specific-task selection.
- Mounted component tests restore a persisted active upload under a new panel device, navigate to a different directory and select the same filename: a new task targets the visible directory and the old task remains failed. A second test exercises explicit reselection, rejects the wrong filename, then verifies the original task/destination is used without duplicating it.
- All 196 Web tests, TypeScript, frontend and embedded server builds passed. No browser visual review or original-file content/hash verification is claimed; reselection matches the existing name/size contract.

## Embedded task controls share the lifecycle rules

- PanelFiles allowed queued uploads to be resumed again or cleared before cancellation. Reused `availableFileTaskActions` for all four controls and for the action handler, which also verifies the task still belongs to the current map. Queued work must be canceled before it can be removed, matching standalone Files. Clear now has a filename-specific accessible name.
- A mounted component regression selects a file and holds upload_ready, verifies only Cancel is available, attempts the disabled actions, cancels and clears, then delivers the old upload_ready. No file bytes are sent and the cleared task stays absent.
- All 197 Web tests, TypeScript, frontend and embedded server builds passed. No additional browser visual review was performed for this state-rule change.

## Native Responses SSE provider

- Added provider/model `api` selection (`openai-completions` default, `openai-responses` opt-in; model overrides provider). Unsupported explicit APIs fail configuration validation rather than silently using chat-completions. Existing CLI/default configuration remains chat-completions. README now documents the actual `api` behavior and endpoint-root format.
- Responses requests use `/responses`, SSE, `store:false`, instructions, flat function schemas with `strict:false`, reasoning effort/summary when configured, usage and multimodal input. Existing tool-image conversion remains available. The shared HTTP transport retains cancellation, idle watchdog, bounded HTTP errors and one retry only before response progress. The semantic SSE parser handles multiline data, text/refusal/reasoning summary deltas, function argument fragments, completed output, failed/incomplete/error and premature EOF; only completed output can produce executable calls.
- Original output items retain encrypted reasoning and assistant phase. They are deep-cloned, persisted as optional JSONL metadata, and replayed only for the same provider ID/base URL/model (stored as a digest). Other providers/models fall back to ordinary assistant/function-call history. The metadata is excluded from chat-completions requests and browser history. Failed/canceled partial responses retain readable text/reasoning while dropping unexecuted calls and opaque replay. Compaction and subagents inherit Config.API through their existing config-copy paths; recent complete response metadata survives compaction.
- References inspected: OMP 75e7801 `packages/ai/src/providers/openai-responses.ts` and its semantic event/replay design; fetched official https://developers.openai.com/api/docs/guides/streaming-responses , https://developers.openai.com/api/docs/guides/function-calling and https://developers.openai.com/api/docs/guides/reasoning . Official stateless reasoning guidance confirms encrypted content under store:false; the compatibility include field is retained.
- Tests cover two HTTP tool rounds with original reasoning replay and image input, usage, phase preservation, snapshot isolation, provider/address/model switches, no chat-format leakage, failed/incomplete/EOF partial history with no tool execution or retry, cancellation, multiline SSE, tool-only partial progress, initial HTTP failure retry, compaction, JSONL roundtrip/UI exclusion and config inheritance/override/rejection. A real local HTTP Responses fixture through the server's actual Agent WebSocket verifies live reasoning/answer, usage and restored history without opaque-state disclosure.
- Full Go tests, full Agent/server race suites, the added WebSocket/config/persistence race regressions and embedded server build passed. No frontend source changed in this step, and the prior 197 Web-test baseline remains. No real upstream-provider credential/API acceptance run or browser visual pass was performed. Responses WebSocket/session reuse, vendor-hosted tool families, Anthropic and full OMP RPC remain unimplemented; this is native Responses SSE support, not complete protocol parity.

## Responses heartbeat and bounded SSE framing

- During the next provider audit, found that Responses refreshed its idle watchdog only on `data:` lines. Comment-only heartbeats could therefore cancel a healthy long-running response. Extracted semantic SSE framing into `provider_sse.go`; comments and metadata refresh transport liveness without counting as assistant progress or suppressing the existing retry rule.
- The framing layer caps the assembled data event at 16 MiB, in addition to bounding individual lines. Previously arbitrarily many short data lines could accumulate without limit. Data fields remove exactly one optional space after the colon and retain other whitespace; CRLF, multiline data, empty fields and final events without a blank terminator are covered. Transport read errors do not dispatch unfinished data; semantic completion stops further reads.
- A virtual-time test drives the actual Session/shared HTTP watchdog with a fixture RoundTripper: five comment heartbeats spaced at one quarter of the idle timeout keep one request alive beyond the timeout, then complete normally. No global timeout mutation or real-time waiting is needed. Further tests cover multi-line aggregate overflow, framing/liveness, error propagation and heartbeat-only premature EOF without model progress.
- Full `go test ./...`, full Agent/server race suites, the subsequently added heartbeat integration regression under `-race`, and embedded server build passed. Logs: `/tmp/rdev-sse-go.log`, `/tmp/rdev-sse-race.log`; review binary: `/tmp/rdev-sse-review`. No frontend code changed and no new UI/real-provider acceptance is claimed. Anthropic Messages remains an investigation, not an implemented adapter.

## Native Anthropic Messages and explicit thinking capabilities

- Added provider/model `api: anthropic-messages`, `/messages`, API-key/version headers, system messages, native tool schemas, consecutive user tool-result grouping, per-round `is_error`, user images and images inside tool results. The existing shared transport supplies cancellation, pre-output retry, idle watchdog and bounded SSE framing. No OAuth token, Bedrock/Vertex or hosted-tool implementation is implied.
- Model `maxTokens` defaults to 8192. Thinking mode is explicit using OMP's `anthropic-budget` / `anthropic-adaptive` names; no name-based capability guessing. Budget effort maps to 1024/4096/8192, capped to leave 1024 output tokens; invalid explicit budgets fail config validation. Adaptive effort uses output_config and the compatibility effort beta; legacy off maps to low. `supportsDisplay` explicitly enables summarized thinking only on supported models. The catalog advertises low/medium/high for adaptive models; the shared composer filters its menu and renders old off preferences as effective low. Catalog parsing retains and validates this capability; old catalogs keep four levels.
- Stream handling assembles text/thinking/signatures, redacted thinking, fragmented tool JSON and cumulative usage including cached inputs. Tools become executable only after valid completion with closed blocks, object input and a compatible stop reason. Matches OMP's terminal-delta EOF fallback when only message_stop is missing. Errors, cancellation, truncation, invalid block/JSON sequences and unsupported hosted blocks do not execute pending calls. Text/reasoning remain readable after failure, without opaque replay. Streaming text/signature/JSON assembly uses builders rather than repeatedly encoding growing blocks.
- Shared `ProviderReplay` cloning/scope logic preserves the existing Responses persistence shape. Anthropic uses a distinct optional JSONL field, replayable only for the same provider/address/model. Signed and redacted blocks remain local/provider-only; browser history and other API serializers never receive them. Replay stably places tool_use after non-tool blocks, following OMP's continuation ordering. Compaction retains recent replay; subagents copy the selected protocol/capabilities into isolated sessions.
- References: OMP 75e7801 `packages/ai/src/providers/anthropic.ts`, `anthropic-client.ts`, `anthropic-headers.ts`; official https://platform.claude.com/docs/en/api/messages-streaming (local inspection copy `/tmp/rdev-anthropic-streaming.txt`). This is explicit common Messages support, not every OMP provider compatibility option.
- Tests exercise real local HTTP two-tool continuation with images/signatures, cache usage, scope switching/snapshot isolation/cross-API exclusion, failure and cancellation, error/image result grouping and reused IDs, compaction, terminal-delta EOF, capability validation and display gating. Actual server WebSocket tests verify config forwarding, live text/thinking, model-catalog capabilities and reconnect history. Fresh JSONL-store load verifies opaque-state persistence and UI exclusion. Two background subagents must reach an HTTP barrier concurrently and return their own task results. Mounted composer coverage parses the catalog and opens the actual menu, including the legacy off preference and old-catalog fallback.
- Validation passed: full Go suite (`/tmp/rdev-anthropic-go.log`), full Agent/server race suites (`/tmp/rdev-anthropic-race.log`), final protocol/config/catalog/WebSocket/persistence race regressions after catalog changes (`/tmp/rdev-anthropic-final-race.log`), all 197 Web tests, TypeScript, frontend build and a fresh embedded server build (`/tmp/rdev-anthropic-review`). A WebSocket test initially assumed the fixture exposed one model; it actually exposes two, so the final assertion checks that only the adaptive model has restricted levels. No real upstream Anthropic credential/API acceptance or new browser visual review was performed; the full completion audit remains open.

## Shared CSV preview, file state isolation and nested image overlays

- Ported BB f51e6b0 `FilePreview.tsx` CSV parsing and table layout into shared helpers/components. Uses the same `@tanstack/react-virtual` 3.14.20, 29px estimated rows, eight-row overscan, 500 data-row/100-column display limits, sticky header/row numbers, column widths, theme tokens and persistent two-axis scrollbars. BOM-prefixed, quoted/escaped/multiline and uneven CSV rows are supported. Preview/source toggle and copying the original loaded text are available in both Agent tabs and standalone Files. Server text/image read limits remain as documented.
- File identity scopes transient preview/wrap/selection/image state; Agent additionally keys by thread/tab identity. Markdown/CSV start in preview unless a line-range intent requires source. Content refresh and scrolling dismiss stale selection actions. Removed a path-only Pierre line-cache key so changed source contents cannot reuse an unrelated cached line set. Header icon targets follow BB's 36px compact coarse-pointer sizing (refresh 40px). Compact standalone dialogs now use full inner width and omit desktop-only close-button top padding.
- File images use the existing BB lightbox/pan/zoom. A real desktop-browser check exposed that a body-portaled lightbox inside the Files Radix dialog inherited pointer-events:none and lost focus to the parent. The lightbox now participates in Radix's modal/focus layer while retaining BB drawer keyboard-stack handling; explicit trigger restoration runs after modal unmount. Closing a nested lightbox leaves its parent file dialog open. Gallery navigation/zoom/reset and wheel cleanup regressions still pass; focus assertions wait for Radix's deferred unmount restoration.
- Six added tests cover parser quoting/BOM/caps, mounted CSV default/source/copy/line intent and file-state isolation, 600x120 input virtualization reaching the final preview row with fewer than 60 mounted rows / 6000 cells, image opening/file switch, and a real nested Radix dialog's pointer/focus/Escape behavior. All 203 Web tests, TypeScript, frontend build and fresh embedded server/client builds passed. Logs: `/tmp/rdev-csv-web.log`, `/tmp/rdev-csv-types.log`, `/tmp/rdev-csv-build.log`; final server binary `/tmp/rdev-csv-review-next`.
- Browser validation used a real local client `csv-review` with synthetic files under `/tmp/rdev-csv-fixture`, server on loopback 19660, no Agent provider and no desktop capture. At 1440x1000 light, a 600x40 CSV mounted 29 rows at the top and 28 at the bottom, reached row 499, and kept the header and row numbers pinned during two-axis scrolling. At 390x844 dark/reduced-motion/coarse-pointer, the table viewport was 356px wide within symmetric 17px margins, mounted 25 rows, and produced no page overflow; controls measured 36px (refresh 40px). Desktop/light and compact/dark screenshots were inspected directly. Native image open/close worked in desktop dialogs and compact drawers; the parent remained open and focus returned to the opener. Actual source highlighting refreshed from “before refresh” to two new lines containing “after refresh” and `updated = true`.
- This closes the bounded CSV-table and shared image-entry gaps, not complete file-preview parity. Video/HTML/external-open surfaces, all multi-device overlays and real mobile hardware remain outside this verification. The complete objective stays open.

## File selection actions through highlighted Shadow DOM

- Followed BB f51e6b0 SecondaryPanelSelectionActions / TimelineSelectionMenu: collision-aware floating action, preserved selection on activation, requestAnimationFrame-coalesced selection/keyboard updates, dismissal on scroll/resize/Escape/content changes, native selection on coarse pointers. Both range endpoints must belong to the current visible panel, including Pierre open Shadow DOM; click revalidates live text before inserting, so stale or cross-pane selections are not submitted.
- Four meaningful regressions cover composed/legacy Shadow DOM ranges and indentation, hidden/cross-pane rejection, keyboard updates and dismissal, stale cross-pane click and pending-frame cleanup. All 207 Web tests, TypeScript, frontend build and fresh embedded server build passed (`/tmp/rdev-selection-web.log`, `/tmp/rdev-selection-build.log`, `/tmp/rdev-selection-review`).
- Real browser used isolated loopback server 19670, synthetic local SSE provider 19674 and real client selection-review, with source fixture `/tmp/rdev-selection-fixture/source.ts`. Programmatically selecting the actual highlighted Shadow DOM line and then natively clicking the action inserted exactly `  return 42;` into the real Agent composer and cleared selection/menu. This was DOM Range selection, not native mouse dragging. At 1440x1000 light the menu fit the panel; at 1100x800 dark/reduced-motion its bounds x759–865 fit the 369px panel x731–1100. Scrolling dismissed the action and retained the draft. Full native keyboard/touch and every overlay combination remain unverified.

## HTML file preview/source parity

- Added HTML/HTM default preview and source toggle to the shared Agent/Files preview, following BB f51e6b0 FilePreview's iframe layout and allow-scripts sandbox. Authored document styles remain intact; no canvas theme overrides are injected. The iframe has an opaque origin, no referrer, and a leading CSP blocking network subresources/connect/form actions. Input is never parsed in the host document. Embedded data/blob images/media and inline scripts/styles work; relative/remote assets are not served by this bounded file-read path. This is not full BB resource-serving parity or external-browser support.
- The same iframe remains mounted but hidden/inert in source mode, preserving form/script state. Refresh changes its srcdoc; changing files replaces the frame. Source copying returns the untouched loaded file, and line-range intents start in source mode. Mounted regression covers these lifecycles, sandbox/CSP, authored styles/language and copying. Full 208 Web tests passed, then the final prefix-policy change passed all five file-preview tests, TypeScript, Vite and fresh embedded server build. Logs `/tmp/rdev-html-tests.log`, `/tmp/rdev-html-final-tests.log`, `/tmp/rdev-html-final-build.log`.
- Real browser at 1100x800 dark/reduced-motion used synthetic provider 19674, real device client and isolated loopback server19670. Native input/click changed a draft and counter, and both survived source/preview roundtrip. Fixture script attempting parent.document was denied. Final rebuilt binary `/tmp/rdev-html-review-next` verified leading CSP document, live interactive frame and isolation; iframe filled the 369x724 panel. No native touch/video/external-open acceptance is claimed.
- Review also exposed an existing restart gap: restored Agent file tabs return `no working directory` until a new model turn activates the session, after which refresh works. This remains a concrete follow-up; it is not counted as resolved by HTML preview.

## Cold-start Agent panel working directory

- Fixed the cold-start race observed above: restored file tabs captured an empty effective cwd while the device default probe was still pending. File reads, diffs, changes, searches and default directory browsing now await a shared bounded cwd discovery outside the socket goroutine and session lock. Concurrent requests share one probe; failed commands do not cache stdout as a path, and later requests retry. Thread metadata waits for discovery too, while explicit thread cwd values bypass that wait.
- New real WebSocket regression restores a JSONL thread, holds the device pwd response behind a barrier, sends two file reads, and receives pong while discovery is pending. Both file replies succeed with their own request IDs after release, using one pwd command and no model turn. A second regression verifies failed-command stdout is rejected, retry succeeds and success is cached. Focused race checks passed.
- Actual browser verification restarted the server with the previous HTML-review data and a real device client, then reloaded the restored Agent page. The counter.html iframe and project directory loaded directly without a message or manual refresh. No provider process was running. Review binary `/tmp/rdev-cold-preview`, loopback19670, 1100x800 dark/reduced-motion.
- Final validation: full `go test ./...`, full server `-race` suite and embedded server build passed. Logs `/tmp/rdev-cold-preview-go.log`, `/tmp/rdev-cold-preview-allrace.log`, `/tmp/rdev-cold-preview-race.log`. No frontend changes in this batch; the broader completion audit remains open.

## Bounded device download ranges (media transport foundation)

- BB f51e6b0 FilePreviewVideo uses a native video with controls/preload=metadata. Rdev's existing WebSocket download accepted a starting offset but always streamed the remaining file, so it cannot efficiently back seeking. Added optional `length` to file_download_start and Files download_start forwarding. Zero preserves whole-file/resume requests; positive values bound device reads, clamp at EOF without offset+length overflow, and echo actual length while size remains the full file size. Negative lengths/out-of-file range offsets fail. Buffers remain bounded to at most 512KiB; cancellation removes the task and an unexpectedly truncated range emits an error, never success.
- Client tests cover a range spanning binary chunks, EOF clamping, empty range at EOF, legacy full/resume downloads, invalid ranges, overflow-sized length, cancellation after the first chunk and file truncation. Full Go tests, full client/server race suites and rebuilt server/client passed (`/tmp/rdev-range-go.log`, `/tmp/rdev-range-race.log`).
- Real TCP client/server and Files WebSocket on loopback19670 read two concurrent ranges from a 2MiB patterned fixture. Offset123 returned exactly700000 bytes; offset2097140 with length100 returned exactly12 bytes. Every byte, frame offset, metadata size/length and binary end marker was checked. The initial ad-hoc harness waited for a JSON terminal event, but this route intentionally completes on the binary end marker and removes routing before the redundant JSON event; correcting the harness verified the actual protocol.
- This is a transport foundation, not completed video preview. Native video HTTP Range serving, authorization, cancellation/backpressure and old-client capability handling must be connected before exposing playback. Old clients ignore the optional field, so callers cannot assume bounded behavior without checking returned range metadata/capability. UI remains unchanged.

## Native video previews with bounded HTTP Range serving

- Agent file_read and authorized Files read now return a scoped media URL for MP4/M4V/WebM/OGV/MOV paths. The common preview renders BB f51e6b0 FilePreviewVideo's native controls, metadata preload, black stage and 34rem height limit, plus inline playback and a recoverable error message. Media URLs are not exposed as text/source-copy controls. Codec support remains the browser's responsibility; there is no transcoding.
- `/api/media/` accepts GET/HEAD using an unguessable 256-bit bearer tied to one device/path/password fingerprint, expiring after one hour. Password rotation revokes access, offline devices fail, and grants are bounded to1024 with reuse for the same active file. No admin/device password appears in the media URL. Up to16 HTTP media requests run concurrently; device reads are at most512KiB with a two-chunk queue and15-second per-read timeout. HTTP Range/HEAD/If-Range use ServeContent over a remote seekable reader; no entire-file cache or browser Blob download is needed. Cancel/error exits clean up routing and cancel the device transfer. Size/modification changes abort playback. Old clients without bounded-range metadata fail with an update-client error rather than silently streaming the whole file.
- Tests cover complete and partial bodies, suffix/open ranges, HEAD,416, invalid/expired grants, password rotation, foreign Origin, unsupported HTTP method, old-client rejection and cancellation cleanup. Mounted UI regression checks controls/preload, no URL-as-source behavior, playback error and refresh reset. Full Go suite, full server race suite,209 Web tests, TypeScript, Vite and fresh embedded server build passed (`/tmp/rdev-video-go.log`, `/tmp/rdev-video-race.log`, `/tmp/rdev-video-web.log`, `/tmp/rdev-video-build.log`).
- Real browser used a generated8-second640x360 WebM, real range-capable TCP client and synthetic Agent provider on loopback19670/19674. Agent1100x800 dark: metadata readyState4, duration8,369px-wide stage; playback advanced to0.2538s and a programmatic media seek completed at6s. Direct browser Range100–199 returned206, Content-Range100–199/501447 and100 bytes. Files desktop modal also loaded duration8/readyState4. At390x844 light the video measured388x218.25px with no document overflow. Playback/seek used browser media APIs; native control drag/touch and other codecs/platforms remain unverified. Review binaries `/tmp/rdev-video-server` and `/tmp/rdev-range-client`; fixture `/tmp/rdev-selection-fixture/video.webm`.
- This completes the bounded native-video path, not HTML relative-resource serving, external-browser opening, all codecs or full BB parity. Server restarts invalidate media grants; refreshing the file preview issues a new URL.

## Persistent panel media/desktop visibility

- Inspection found that closing Agent's side panel collapses/hides it rather than unmounting it. Active desktop/terminal/files tabs previously received only tab selection as visibility, so an active desktop could keep its connection/input listeners while the entire panel was closed. The owning panel's open state now participates in child visibility and is passed to video previews. Persistent terminal/file state remains mounted.
- Video effects explicitly pause, remove src and load on hide/unmount, aborting media requests and releasing the decoder. Reopening reinstalls the URL and restores a valid saved position after metadata, without autoplay. Initially hidden previews issue no media request. Browser-tab visibility itself is not overridden.
- Added mounted regressions for initially hidden media, pause/load/source cleanup, saved position, no autoplay, unmount cleanup, and persistent desktop visibility across parent close/reopen.211 Web tests, TypeScript, Vite and embedded server build passed (`/tmp/rdev-media-lifecycle-web.log`, `/tmp/rdev-media-lifecycle-build.log`). No Go source changed.
- Real browser at1440x1000 light/reduced-motion used the prior video fixture and real range client. Closing the panel left video mounted but paused with no src and networkState0. Reopening after a paused3s seek restored3s, paused, readyState4. The initial play check reached the end of the8s fixture before the next interaction; the deterministic paused-position roundtrip supplied restoration evidence. Desktop visibility was verified at the mounted component contract, not a new desktop hardware capture.

## File socket task/request namespaces

- Found that Files global routing maps used browser-provided task/request IDs directly: two windows submitting the same ID could overwrite each other's routes. Each Files WebSocket now receives a random private wire prefix. All incoming text IDs and binary task IDs are scoped before routing; outgoing text/binary frames restore the original browser ID. Device protocols and browser transfer state require no format change. Generated IDs follow the same translation, and IDs exceeding the binary protocol's255-byte wire limit are rejected before dispatch.
- Real two-socket regression covers same-ID parallel tasks, correctly owned text metadata and binary chunks, JSON cancellation and binary fallback, inbound binary cancellation, reverse-order same-ID directory replies, generated-ID cancellation and oversize rejection. Tests run under race detection. The existing slow-close/nonblocking-workspace and operation error identity tests remain passing.
- Real TCP client plus two Files sockets used the same task ID to concurrently fetch offset123/length700000 and offset2097140/length100 from a2MiB fixture. They received700000 and12 correct bytes with unchanged public IDs. Two same-ID uploads to distinct temporary files completed with exact independent `workspace A` / `workspace B` bodies and correctly translated binary acknowledgements. Scripts `/tmp/rdev-files-namespace-check.mjs`, `/tmp/rdev-files-namespace-upload.mjs`; temporary directory `/tmp/rdev-namespace-upload-q2OTeD`.
- Full Go tests, full server race suite and rebuilt server passed (`/tmp/rdev-files-namespace-go.log`, `/tmp/rdev-files-namespace-race.log`, `/tmp/rdev-files-namespace`). No frontend changes. This isolates connections; callers must still use distinct IDs for overlapping attempts within one socket. Concurrent uploads targeting the same physical destination remain a separate file-write ownership concern, not proved by the distinct-file test.

## Device upload destination ownership and lifecycle

- Follow-up confirmed that distinct transfer IDs targeting one file opened the same `.rdevpart` concurrently. Moved managed uploads into a focused client module with per-upload handle/offset/terminal-state locking and a separate short-held map lock. Absolute paths and resolved parent-directory symlinks identify destination ownership; Windows keys are case-folded. Only one active task may own a destination, including during final sync/close/rename. A competing task receives an explicit busy error before opening/truncating the partial file. Different destinations do not share a file-I/O lock.
- Chunk, completion, cancellation and disconnect now serialize against the same upload state. Cleanup releases only its own task generation/path reservation, retains resumable partial bytes and never publishes an incomplete destination. Delayed starts check their originating connection before reservation; disconnect snapshots tasks and closes them outside the global client lock. Existing whole-file and resumable protocol shapes remain unchanged.
- Tests cover two simultaneous same-target starts, symlink directory aliases, disconnect/resume preserving partial data, independent progress while another upload's I/O lock is held, and30 completion/cancellation/write races. Full Go suite, full client race suite and rebuilt client passed (`/tmp/rdev-upload-owner-go.log`, `/tmp/rdev-upload-owner-race.log`, `/tmp/rdev-upload-owner-client`). No frontend/server source changed.
- Real client/server plus two Files sockets uploaded to the same temporary target. Owner wroteABC, competing socket received `upload already active for this task or destination`, owner wroteDEF and committed exactlyABCDEF (`/tmp/rdev-upload-owner-SVZdcc/same.txt`, harness `/tmp/rdev-upload-owner-check.mjs`). Validation is Linux within one client process; cross-process writers, Windows/macOS filesystem variants and all non-upload mutation paths are not covered by this ownership map.

## BB file-preview header and narrow action menu

- Replaced the old flat icon toolbar using BB f51e6b0 FilePreviewHeader/FilePreviewChrome as the source: surface-raised36px header (48px compact/coarse), mono medium file-accent path with leading truncation and click-to-copy,20px desktop icon controls, bordered Preview/Source segmented buttons and a560px actual-pane-width breakpoint. Narrow panes move refresh/copy/wrap to the shared responsive menu; compact coarse-pointer segment height36px and action trigger40px match BB. Tooltips use300ms delay and bottom placement, selected/wrap states use shared Button rules, and reduced motion disables loading rotation.
- Copy results now expose a success live region/check icon and a visible failure message. Concurrent clipboard completions use a revision guard; old promises cannot overwrite newer results or update an unmounted header. Path/content copying still uses the original string. Header state lives with the existing file identity; viewport width alone does not decide whether a narrow Agent pane gets its menu.
- Tests cover559/560px resize, segmented state, keyboard menu opening, menu copy and disabled refresh while loading, copy rejection and stale completion ordering. Existing media-error assertions now target the media region rather than the newly added clipboard live region. All213 Web tests, TypeScript, Vite and final embedded server build passed (`/tmp/rdev-file-header-web.log`, `/tmp/rdev-file-header-final-build.log`, `/tmp/rdev-file-header-final`).
- Real Files HTML preview at1440x1000 light had its desktop screenshot inspected. At390x844 dark the menu opened as a nested drawer, copying reported success, and the parent file drawer stayed open while the menu became closed/aria-hidden. A narrow screenshot tool call stalled and was terminated; narrow evidence uses rendered DOM measurements and native interactions. Touch emulation confirmed pointer:coarse,36px segment heights,40x40px action button and no document overflow. The final tiny icon adjustment restores BB's3.5-size preview glyph; final build passed after that adjustment.
- This aligns the existing file-header actions; external-browser/open-in-editor actions and HTML relative-resource serving remain separate incomplete capabilities.

## HTML 外部浏览器预览（2026-11-08）

- 参照 BB f51e6b0 的 FilePreview 外部 URL 分支，仅为完整 HTML/HTM 增加浏览器图标；Agent 和 Files 共用工具栏，窄面板保留入口。
- 服务端单文件授权 URL 返回 CSP 响应头 sandbox allow-scripts，禁止 same-origin、网络和表单提交；链接 noopener/noreferrer。超限文件拒绝返回，不渲染截断网页。
- 真实设备客户端与嵌入前端服务验证：Files 原生点击链接打开独立标签页；页面记录 openerAbsent=true，storage/cookies/network 均 blocked。键盘 Tab/Enter 将页面计数器从 0 改为 1，证明内联脚本正常运行。DevTools 点击该沙盒页面按钮超时，键盘操作成功；不把该点击路径记为通过。
- 父页面预览保留；390px 暗色视口文档宽度仍为390px，浏览器入口位于 x=233、宽20px，未溢出。桌面1440px亮色入口已实际打开。此轮窄屏不含真实触屏验收。
- 213 Web 测试、TypeScript、Vite build、go test ./... 和服务端全量 race 通过；测试包含 GET/HEAD、精确 HTML 正文、沙盒响应头、超限取消与任务清理、URL 过滤及窄宽布局。
- 边界：仅单文件内联资源；相对路径/远程资源仍不提供，未实现打开编辑器。授权链接一小时后过期，可刷新获取。

## 后台任务最终详情（2026-11-08）

- 对照 OMP 75e7801 的 builtin-session jobs 命令与本地任务面板检查时发现：运行中 get_jobs 详情已将缺失 result 解析为空字符串，之后 jobs 列表保留该值；旧的 result !== undefined 判定误以为最终详情已加载，导致完成时不再读取最终输出。
- 现在详情回复单独记录 detailState，列表汇总只更新任务状态并保留详情来源状态。完成、失败或取消必须收到对应状态的详情，才停止该任务的详情轮询；运行中已读内容不能冒充最终详情。明确加载过的空结果显示“任务已结束，没有输出”，不再显示等待输出。
- 回归覆盖 running → completed/failed/canceled 的列表/详情序列、最终输出保留、空字段被 omitempty 省略、面板继续读取最终详情及读取后停止详情轮询。列表轮询仍按既有策略保留，以发现新任务；隐藏/断线停止轮询行为保持。
- 全量224项Web测试、TypeScript检查、Vite构建通过。本轮验证为状态Hook和实际JobsPanel的DOM/计时器回归，未新增远端后台任务实机验收。

## 并发图片附件顺序（2026-11-08）

- 图片原先在每个 FileReader 完成时直接追加，读取顺序与选择顺序不一致时，缩略图和发送给模型的附件会重排。
- 现在并发读取保持不变，以选择顺序保存读取项，只提交已经连续完成的前缀。失败项提示并跳过，释放后续成功项；切换会话/卸载清空读取队列，晚到回调仍被忽略。待处理图片继续占用四张附件配额并阻止提前发送。同步 readAsDataURL 异常也按失败收尾，不留下永久等待状态。
- 实际 ChatPaneView DOM 回归模拟同次选择的倒序完成，断言预览和发送 payload 都维持选择顺序；另覆盖连续两次选择、前项失败后项成功、同步读取异常，保留既有切换会话与卸载取消回归。测试有意控制 FileReader 完成顺序，不把它描述为真实系统文件选择器验收。
- 全量224项Web测试、类型检查和Vite构建通过；对照BB的图片查看器与图库规则保持不变，此次修复rdev附件读取时序。

## 透明附件与 BMP 历史预览（2026-11-08）

- 发送附件的历史预览现在检测解码后的透明度：有透明像素时保存 PNG，避免透明图被永久铺白；不透明附件继续生成 JPEG。两者沿用最大640px边长、192KiB编码内容及1600万像素解码前检查，超过编码上限继续缩小，无法满足时不存预览。
- 注册已有 golang.org/x/image 的 BMP 解码器，使浏览器能够显示的 BMP 附件也能生成有界历史预览。未加入新依赖；这不代表支持所有浏览器格式的服务端解码，例如 AVIF/SVG/ICO 仍未补齐历史预览。
- read_file 工具输出保留原 JPEG 预览契约和8KiB上限，避免改变 provider_images 的图片识别。透明文件的原始文件预览依旧由浏览器显示。
- 回归验证透明和半透明像素的 alpha 值、JPEG工具预览、BMP预览、有界PNG缩小和不可能尺寸拒绝。已有历史序列化/磁盘重载测试现使用透明PNG，确认预览持久化但原始附件仍不持久化。
- 全量 `go test ./...` 与上述图片/ReadFile针对性竞态检查通过；本轮未修改Web代码，未重复浏览器视觉验收。

## 浏览器格式附件转 PNG（2026-11-08）

- Web 添加 AVIF、SVG、ICO（x-icon/vnd.microsoft.icon）附件时，利用浏览器解码并转为PNG后发送。保留文件名，MIME/data/缩略图使用转换后的PNG；普通PNG/JPEG/GIF/WebP/BMP保持原字节。这样现有模型输入和有界历史预览共用PNG支持，无新增协议字段，不需要持久化原图。
- 保留原尺寸，PNG编码超过4MiB时逐次减半尺寸；超过1600万像素、解码/画布失败或10秒加载超时提示读取失败，不发送空图片。转换占用附件名额并遵守原选择顺序；切换设备/会话、发送清理或卸载会中止转换，晚到结果不污染新草稿。动画/多帧格式取浏览器解码的静态画面，不保留SVG矢量信息。
- 真实Chromium实际附件输入流程验证：SVG 80×60转PNG，透明/半透明alpha分别0/128；发送后刷新，历史预览恢复为80×60 PNG且alpha保持。另同批添加32×32 ICO/AVIF，两者均转PNG，alpha=128，顺序保留。输入通过内存File+change事件注入；原生上传工具因workspace路径权限拒绝，未宣称原生文件选择器通过。固定提供方未做视觉理解，不代表上游模型视觉效果验收。
- 232项Web测试、类型检查、Vite构建与Go嵌入服务构建通过；测试覆盖MIME分支、尺寸保留、超限缩小、取消/超时/解码/画布异常和资源清理。
- 边界：依赖浏览器支持相应源格式；直接API客户端若绕过Web转换，服务端依旧不能原生解码AVIF/SVG/ICO。原始发送图仍不写入持久历史。

## 图片组变动时的全屏选择（2026-11-08）

- 原图片预览按数组下标保存选择，图库增删/重排时会跳到另一张图，缩短后页码可能越界。现在按图片src/name保持选择；当前图片已不在列表时回到触发图片，缺失/空图库仍包含有效触发图片。触发图片本身替换时关闭旧预览，避免组件复用沿用上一张的全屏状态。
- DOM回归使用实际ImagePreview和ImageLightbox验证方向键导航、重排保留选中图片、删除后回退及准确页码、触发图替换关闭、空图库回退。保持BB循环导航、缩放和主题样式不变；本轮未重复实际浏览器触屏矩阵。
- 全量233项Web测试、类型检查与Vite构建通过。

## 终端启动尺寸与输出路由（2026-11-08）

- 远端PTY以80×24启动，而xterm可能在WebSocket连接前已经fit，认证/重连后尺寸未变就不会再触发onResize。终端服务现在在注册会话并成功发送启动请求后发出 `ready`；Terminal页面和Agent终端面板收到后显式同步当前rows/cols，隐藏面板同步尺寸但不抢焦点。`auth_ok`路径也同步尺寸；旧服务端不发送ready时仍保留原onResize行为，不能声称旧服务端免密初始尺寸缺口也已修复。
- 服务端先预留会话配额和注册输出路由，再向设备发送启动请求。避免设备立即回传的首段输出丢失，达到会话上限时不启动远端进程；启动发送失败清理全局与设备会话映射。
- 真实Chromium与Linux客户端（DISPLAY=:98）验证：1440×1000视口首次终端同步63×147，原生输入 `stty size` 远端返回63 147；向右分屏后新终端同步63×72，返回63 72。浏览器测试只记录fixture终端的resize帧和输出；不涉及真实桌面DISPLAY=:0。
- WebSocket回归覆盖免密/密码认证、设备启动时立即输出、ready后尺寸转发、配额拒绝与发送失败无名额泄漏；针对性race通过。Agent终端面板回归验证隐藏不抢焦点、认证/重连明确发送尺寸。233项Web测试、类型检查、前端及嵌入服务构建通过。
- 首次全量Go检查与Vite资源替换重叠而出现embed文件缺失，已在Vite结束后单独重跑，`go test ./...`通过。该失败属于验证调度错误，不是产品运行时失败。

## GPU MediaSource 流生命周期（2026-11-08）

- 旧MediaSource的sourceopen原先不检查归属，可在关闭/替换后创建SourceBuffer并覆盖当前ref。现在将sourceopen/updateend监听器作为同一个流的生命周期管理；回调检查当前MediaSource与视频模式，关闭时移除监听器，旧流事件不再操作新队列。
- SourceBuffer建立后立即调用队列处理，不要求再收到下一包才追加此前排队的数据；addSourceBuffer失败转为界面错误状态，不让浏览器事件抛出未捕获异常。sequence模式不支持时保留原降级行为。
- 回归用EventTarget模拟浏览器媒体事件，覆盖首批排队数据启动、updateend继续处理、sourceopen一次性行为、替换前后的晚到事件、关闭清理及不支持编码格式的异常路径。241项Web测试、类型检查和Vite构建通过。
- 本轮未运行硬件GPU视频源或Safari ManagedMediaSource实机验收；这些仍是审计边界，不将事件模拟描述为真实视频播放验证。

## Agent 文件分页失败重试（2026-11-08）

- 对齐独立Files页面的已有行为：Agent文件面板加载下一页失败时保留已加载文件、total/offset和加载更多入口，在列表上方显示错误；重试清理错误并请求相同offset。首次目录加载失败仍清空旧内容及分页ref，避免展示错误目录。
- 实际PanelFiles DOM/WebSocket模拟回归覆盖首批成功→下一页失败→同offset新请求重试→旧失败重复回包→第二页成功，确认文件完整且没有重复、错误提示清理；另覆盖刷新失败后分页从0开始。
- 244项Web测试、类型检查及Vite构建通过。本轮未新增真实远端网络故障注入，既有请求归属/设备切换/传输回归继续通过。

### Composer attachment errors and explicit commands

- Attachment count/size/read failures now use a composer-local alert instead of `pushBubble`, whose transcript lifecycle closes the active turn. Draft edits, attachment removal, successful sends and thread/device switches clear the local error; canceled readers retain their existing ownership guards.
- `/compact`, `/bg` and `/subagent` with attachments are rejected in the composer while preserving text and images. Server dispatch rejects the same commands before history/model/tool mutation. Other slash-prefixed conversation text still follows normal chat dispatch.
- Actual ChatPane regression covers uppercase/multiline commands, retained attachments/drafts, thread-switch alert cleanup and failed image reading during a running turn without transcript insertion. Server regression covers all three commands, unchanged history and no tool execution/events.
- Validation: Web suite 244 tests / 65 files, TypeScript and Vite production build passed. The final focused composer regression passed after adding the running-turn fixture reference. This change has no new real-browser visual acceptance claim.
- `go test ./...` and focused server command/compaction tests with `-race -count=1` passed after the Web build completed.

### OMP-style running subagent guidance (tool entry point)

- Compared OMP `75e7801` RPC `wire/commands.ts`'s `steer_subagent` contract (running child ID + message). rdev now exposes `jobs` tool `action=steer`, `id`, `message`; this is behavior alignment, not a claim of OMP wire compatibility.
- Per-child bounded FIFO guidance queues use existing `RunTurnWithSteering` at settled response/tool boundaries. Final admission closes atomically with checking for remaining input. Late accepted guidance starts another turn using the same isolated child session and original granted capabilities, within the existing 30-minute child deadline.
- Shell jobs, unknown/cross-thread IDs, completed/canceled/closed children reject guidance. Queue limit is 8 messages of at most 4096 bytes each. Cancellation/failure preserves unconsumed guidance in explicit job detail/completion `pendingGuidance`; list summaries omit it. Detail snapshots copy pending strings so callers cannot mutate stored state.
- Integration tests hold an HTTP provider's first response, submit two actual `jobs` tool requests and verify the next provider requests contain the ordered user guidance in the same history. Both normal MaxTurns=8 and last-boundary MaxTurns=1 cases return the final answer. Separate tests cover completion/admission racing, cancellation retention, limits, shell rejection, cross-thread rejection and snapshot isolation.
- Direct task-panel guidance input and the OMP RPC endpoint remain outstanding. No live upstream model or real-browser claim is made for this addition.
- Validation: `go test ./...` passed; focused subagent/guidance/job/cancellation suite with `-race -count=1` passed, then passed again after the final snapshot-copy change and isolation assertion. No Web code changed in this slice.

### Direct task-panel subagent guidance

- Added a shared-themed textarea and Send guidance button for selected subagent tasks. Each task/thread has its own panel-local draft. Receipt success clears only the submitted draft if it has not since been edited; rejection, disconnect or a 15-second unconfirmed receipt retains it. Finished tasks retain entered text for copying. Guidance drafts are not persisted across page reloads.
- `steer_job` carries request `id`, target `jobId`, `thread` and `text`. `job_steered` echoes those identities with an optional rejection. Replies go only to the requesting connection; per-sink callback matching does not change foreground turn state. Reset/disconnect and component disposal settle/clean outstanding requests. Shell tasks have no guidance form. Explicit job details show unconsumed guidance; list refresh preserves detail and a new detail reply can clear it.
- Real WebSocket test verifies admission, correlation, wrong-thread/unknown-job/missing-request-ID/canceled-job rejection and no foreground turn. Actual React form tests cover overlapping task identities, editing before receipt, stale/wrong-target acknowledgement, queue timeout, disconnected transport, UTF-8 size validation, completed tasks and cleanup. Sink tests cover routing, reset and pending-detail replacement.
- Chromium page 35 used a temporary Vite harness mounting the real JobsPanel and ThreadSink, with simulated 200ms success acknowledgements. At 390×844 touch/light, native fill and click submitted Chinese guidance, then displayed “指导已加入队列。” with an empty textarea; viewport and document width were both 390px, textarea width 340px and font 16px. At 1200×900 dark, form width was 622px, font 13px, document width 1200px; computed foreground/background were oklch(0.81 0 0) / oklch(0.195 0 0). This is browser interaction/layout measurement evidence, not a real-model or pixel-perfect BB acceptance.
- The native window resize call failed with “Active contents not found”; viewport emulation succeeded. Screenshot capture failed with “Unable to capture screenshot”. No new visual screenshot acceptance claimed. Temporary harness files were removed.
- Validation: 247 Web tests / 66 files, TypeScript, production build, `go test ./...`, and focused guidance/subagent tests with `-race -count=1` passed. Vite build completed before Go checks started.

### Non-disruptive command validation and multiline device selection

- Invalid `/jobs` syntax now uses the composer alert instead of a system transcript bubble. Missing `/bg`, `/subagent`, `/steer` arguments and unavailable `/compact`, `/steer`, `/stop` states explain why execution did not happen, retain the draft and do not send a model request. Alerts use the shared `destructive-text` theme token.
- Actual ChatPane integration exercises Enter during a running turn and inactive commands while idle. It checks retained drafts, no transport payload, no transcript bubble insertion and the still-open turn marker. Existing missing-argument paths and successful retries remain covered.
- `/bg --device` recognizes the same Unicode whitespace boundaries as command splitting, including newline, NBSP and em-space, while preserving the shell command's internal newlines. Similar prefixes such as `--device-name` remain ordinary shell text. Invalid/missing targets and empty commands are rejected by parser regression tests.
- Web validation: 247 tests / 66 files, TypeScript and production build passed. This slice has no new browser visual acceptance claim.
- `go test ./...` and focused explicit-command/compaction tests with `-race -count=1` passed after the production Web build.

### Background cancellation request versus completed cleanup

- Job snapshots now expose `cancelRequested` while a canceled worker is still unwinding. A shared manager-locked cancellation method handles one job, all jobs and closure idempotently. The job remains `running` for quota purposes until its worker returns; final snapshots clear the transitional flag and report `canceled`.
- After receipt/list refresh, JobsPanel shows “正在停止…” and disables the Stop button. Subagent guidance is disabled while stopping, with any entered draft retained for copying. The count remains truthful about occupied slots. No optimistic completion or automatic retry is introduced.
- A deterministic server test holds all 8 workers in cleanup: single cancellation does not affect other jobs, repeated cancel-all stays pending, reads expose the flag, replacement admission is rejected, and only actual exit releases the slots. The real WebSocket guidance test now also checks the cancellation reply while cleanup is held, followed by rejected guidance. React tests check disabled actions, retained drafts and transition from stopping to canceled snapshots.
- Web suite: 248 tests / 66 files; TypeScript and production build passed. No new browser visual acceptance claim for this state-label change.
- `go test ./...` and focused cancellation/job-guidance tests with `-race -count=1` passed after the Web build completed.

### Task lifecycle invalidation across browser subscribers

- Added thread-scoped `jobs_changed` notifications for job admission, cancellation request and completion. Each visible JobsPanel refreshes the list and any selected unfinished detail immediately; hidden documents do not fetch until visible again. Existing periodic polling remains the recovery path; this change does not claim reduced polling traffic or measured end-to-end latency.
- Notifications carry no snapshot. The server checks thread/manager ownership before publishing; even a reset racing the final write only causes a fresh read, not reinsertion of old jobs. Repeated cancellation does not publish another change. Callbacks run after releasing the jobs mutex, so reading counts/details cannot deadlock.
- Two real WebSocket subscribers receive each lifecycle invalidation without requesting a poll. After manager replacement, invoking the old publisher yields no invalidation, and both clients read an empty current job list. A separate test exercises reentrant manager reads and idempotent cancellation notifications. Foreground sink tests show invalidations only increment refresh revision, preserving the open turn and cached output.
- JobsPanel tests verify immediate list/detail requests without advancing the timer, hidden-page suppression and visibility recovery. Web suite: 249 tests / 66 files; TypeScript and production build passed. No new visual screenshot or real-upstream-model acceptance is claimed.
- `go test ./...` and focused lifecycle/cancellation/guidance tests with `-race -count=1` passed after the Web build.

### Nonblocking, coalesced job notifications

- Production job change callbacks now enqueue a service-tracked publication worker instead of synchronously writing every socket and scanning sidebar history before returning the job ID/cancel response. Each manager has one active publisher and one pending-change bit. Changes during publication request a fresh follow-up publication, without retaining event payloads or spawning a goroutine per event. Idle queues release their worker and restart on demand.
- Publication still validates current manager ownership; a closed/replaced manager skips both task invalidation and sidebar scan. Reset races cannot restore an old snapshot because notification data remains invalidation-only.
- A real WebSocket fixture holds the server connection's actual write mutex to simulate a blocked subscriber. Job admission, cancellation and worker completion return while it remains locked; after release, a detail request reads the final canceled state. This is controlled backpressure regression evidence, not a real network throughput benchmark.
- A race-enabled concurrent test submits 1000 invalidations while the first publication is held. They produce one pending follow-up (2 publications total), then a later idle request starts a new worker (3 total). Existing two-subscriber lifecycle and manager-replacement tests continue to pass. Relevant server fixtures wait for tracked publication work before tearing down.
- No frontend files changed in this slice; the previously verified 249-test Web build remains the frontend baseline.
- `go test ./...` and focused publication/lifecycle/slow-subscriber/deletion/steering tests with `-race -count=1` passed.

### BB file-link highlighted-line navigation

- Compared BB `f51e6b0` FilePreview's `FilePreviewCode` → `SourceCodeHost` with `scrollToHighlightedLines`. rdev previously supplied highlighted lines to Pierre but did not reveal their position. FilePreview now uses Pierre's public `onPostRender` callback and an animation-frame layout read to locate the requested Shadow DOM row.
- Scrolling changes only the preview body's `scrollTop`, never `scrollIntoView` on ancestor dialogs or the conversation. Each rendered host/line-range target is revealed once, so asynchronous highlighting and theme updates do not undo manual scrolling. Hidden/zero-height panes wait; new target ranges and later visibility can retry. Unmount cancels pending frames and detached/foreign hosts are rejected.
- Hook regression covers actual shadow-root lookup, target changes, repeat renderer updates, hidden/zero-height panes, invalid/missing rows, foreign containers, outer-scroll preservation and cleanup. Existing FilePreview tests remain green.
- Chromium page 35 mounted the real FilePreview/Pierre renderer in a temporary Vite harness with 500 TypeScript lines. At a 1200×900 viewport the file body was y=56..470 (height 414px): line 250 revealed at scrollTop=4300, line 400 at 7000; both rows occupied y=254..272, centered in the pane. Page scroll stayed 0. Manually setting the body to 123 and changing theme preserved 123. With visible=false, requesting line 100 preserved manually set 321; visible=true revealed at scrollTop=1600, row y=254, page scroll still 0. The harness drives line-range props, not a live model/tool link, and no new screenshot acceptance is claimed.
- Temporary harness files and Vite process were removed. Validation: 250 Web tests / 67 files, TypeScript and production build passed. No Go changes in this slice. A remote editing workflow is still outstanding; no placeholder “open in editor” action was added.

### Multimodal steering, including image-only guidance

- Found that the steer button sent only text while leaving selected images in the composer; the server steering queue and `EventUserInput` also omitted attachments. Added images throughout admission, FIFO queue consumption, model-bound message and user-input event. Idle steering uses the normal admission path with already-decoded images, avoiding a second decode/preview conversion.
- The steer button now waits for pending image reads/conversions and supports image-only input. Attached `/steer` commands follow the same path and strip the command prefix. Failed transport sends retain text/images; success clears both. Commands that do not accept images remain rejected before sending. The existing subagent-guidance panel remains text-only.
- Live/replay `steering` frames carry image names and bounded previews, not original image payloads. History retains the same bounded previews as normal chat. The existing active-turn timing and FIFO semantics are unchanged.
- A real WebSocket + local SSE-provider integration test holds the original response, rejects invalid base64 without queuing or canceling it, queues pure-image guidance, verifies original image bytes in the next multimodal provider request, checks the live frame and reconnect replay, and confirms saved history has a preview but no original image bytes. It then sends idle image guidance and verifies the normal-turn fallback also reaches the provider with images. This is not a real upstream model visual-quality acceptance.
- Actual ChatPane tests cover reading-disabled controls, image-only and image+text guidance, both `/steer inspect` and image-only `/steer`, failed sends retaining drafts and successful sends clearing them. Sink regression verifies image-only user bubbles and preservation of the working timer.
- Validation: 251 Web tests / 67 files, TypeScript and production build passed. No new browser screenshot acceptance claimed.
- Go validation: `go test ./...` and `go test -race ./internal/server ./internal/agent -run 'TestImageOnlySteering|TestAgentQueueSteer|TestSteer|TestResetRejectsLateTurn' -count=1` passed after the Web production build completed.

### Independent file preview/source reading positions

- Compared BB `f51e6b0` FilePreview's Markdown scroll-position context and retained HTML preview/source behavior. Shared Agent/Files preview now retains separate mounted scroll containers for Markdown/CSV preview and source after each mode's first use. Source highlighting remains lazy until requested. File identity changes reset these local positions; this is not persistence across refresh/loading remounts, closed tabs or page reloads.
- Inactive text panes are invisible, inert and aria-hidden. They retain layout geometry: a real Chromium experiment with `display:none` moved a virtual CSV from scrollTop 5800 to 6438 because hidden rows were measured at zero height. Preserving the inactive pane's geometry eliminated that drift while keeping it outside normal flow and pointer/focus interaction. HTML retains its existing sandbox iframe; line reveal now targets the dedicated source scroller.
- Actual component regression checks lazy source mounting, renderer identity, independent vertical/horizontal scroll positions, panel visibility changes and new-file resets. Existing HTML, image, video, selection and line-reveal tests remain green.
- Chromium page 35 mounted real FilePreview/Pierre/CSV in a temporary Vite harness. At 1200×900 dark/reduced-motion, Markdown preview/source remained at 1200/800 after toggles. A new line-250 target revealed at source scrollTop 4295, row y=263..281 in a y=60..484 container (424px height), with page scroll 0. A 400-row CSV retained the same table instance and scrollTop 5800 before/after toggling, with 30 virtual rows; its inactive scroller remained 390px high, invisible and inert.
- At 390×844 light/touch emulation, after source highlighting loaded, the same Markdown toggle retained 1200/800; document and viewport widths were both 390 and page scroll remained 0. This is DOM geometry and interaction evidence, not screenshot/pixel acceptance or a real touch-device test.
- Validation: 252 Web tests / 67 files, TypeScript and production build passed. No Go changes. Temporary harness files and Vite process removed after validation.

### HTML relative assets through confined device reads

- Compared BB `f51e6b0` FilePreview's URL-backed `IframeFilePreview` and retained HTML/source modes. Agent and Files now use the authorized preview URL inside the sandbox, with the previous inline `srcDoc` fallback when no URL is present. Changed HTML content reloads the URL-backed frame; mode/visibility changes preserve its instance.
- New clients advertise `confinedDownloads` on both stream and WebSocket registration. HTML grants on those devices authorize web assets beneath the opened HTML's parent directory. Each bounded download supplies `downloadRoot`, opens via `os.OpenInRoot`, and echoes the root in metadata; the server rejects missing acknowledgement before serving bytes. Legacy devices receive a separate single-file grant and retain inline HTML behavior. Capability downgrade cannot expand a grant.
- Relative CSS, JS/MJS imports, images, fonts, audio/video and JSON use native browser resolution under the grant URL. CSP limits resource/fetch sources to that exact grant path, keeps an opaque sandbox origin and disallows host APIs, external origins, base overrides and forms. Opaque-origin CORS is confined to this bearer endpoint and never credentialed. Root traversal, backslashes, drive paths, hidden segments, non-web extensions and directory listing are rejected; device-level symlink escapes are blocked. Each read remains bounded to 512KiB with existing 15-second read timeout and 16 HTTP request slots. HTML remains capped at the existing file-preview limit.
- Regression tests cover relative resources/MIME/CORS, traversal rejection before device access, wrong origins, missing root acknowledgement, password rotation/expiry, legacy inline fallback and capability upgrades. Client tests perform real in-root reads and reject parent/absolute/symlink escapes without sending file bytes. Actual React tests cover URL versus srcDoc, sandbox attributes, iframe identity and reload only when content changes.
- Fresh server/client binaries ran on loopback 19700/19701 with synthetic site files under `/tmp/rdev-html-fixture/site`, no model provider and DISPLAY=:98. Native Files path input and filename opening rendered the page. Chromium confirmed CSS/image/module/import/JSON requests returned 200 and displayed `module loaded 42; host API blocked=true`. The deliberately attempted host API fetch was blocked by CSP. A synthetic page report, matched to the current iframe source window, confirmed computed background rgb(12,34,56) and decoded SVG naturalWidth 64. This report is test-fixture instrumentation, not a product cross-frame API.
- At 390×844 light/touch emulation, iframe width was 388px and document width 390px. Preview/source controls retained the identical iframe. At 1200×900 dark emulation, the real dialog's iframe was approximately 971×556px, inside the viewport; CSS/image/module evidence passed again after capability negotiation was added and both processes restarted. A separate top-level tab also rendered the module/JSON result, CSS background and decoded image, and accessing localStorage threw under the header sandbox. No screenshot/pixel or physical mobile-device acceptance is claimed.
- Validation: 253 Web tests / 67 files, TypeScript, production build, fresh server/client builds, `go test ./...`, and focused HTML/media/confined-download/legacy/download-range checks with `-race -count=1` passed. Go checks/builds ran after Vite completed. Browser fixture processes were stopped after verification.
- Boundaries: no project-root absolute-URL rewriting, outside-directory references, external/CDN resources, arbitrary dynamic backend, dev-server proxy, service worker or full BB host/editor integration. Windows/macOS confinement has not had real-hardware acceptance in this slice.

### HTML preview chrome and real HTTP failure feedback

- Found the toolbar still accepted only legacy `/api/media/<token>` URLs after directory-backed previews were added, hiding Open in browser for new grants. The toolbar and iframe now share one local-grant URL validator: legacy and safe encoded entry filenames work; remote/scheme-relative/script URLs, query/hash suffixes, encoded path escapes and malformed encodings are rejected.
- Matched BB `f51e6b0` FilePreviewIcon's 160ms delay and reduced-motion styling. Pending HTML loads animate the file icon without disabling refresh or source controls. A content/URL revision replaces the iframe and cancels obsolete work; theme/mode/visibility changes retain it.
- Iframe load events do not distinguish HTTP failures. A cancellable HEAD admission check now reports offline/expired/error responses with a localized error and retry button before mounting the remote document. Directory-preview HEAD serves headers after its one-byte metadata probe, skipping the HTML body. This adds one bounded metadata round trip per document revision, not per mode switch. An error between successful HEAD and subsequent iframe GET can still be displayed by the iframe itself; this does not claim detection of every subresource/network/script error.
- Tests cover 160ms loading transition, loaded/reset states, old frame events, failed HEAD/retry/offline disablement, aborted late probe failures, no extra probes on mode switches, URL validation and directory-grant toolbar links. Go regression checks HEAD performs only one bounded device read and returns no body.
- Chromium page 35 used the real FilePreview with a temporary Vite HTTP fixture returning 503 or delayed 200 responses. Initial HEAD 503 displayed the actual localized error and Retry. Native Retry switched to success: observed icon transitions were idle at 2220ms, spinning at 2399ms and idle at 3639ms relative to observation setup (about 179ms delay including render scheduling). One successful HEAD and one iframe GET occurred, then repeated source/preview toggles kept the same iframe with no additional requests. The 1200×900 dark preview frame measured 900×444px. At 390×844 light/touch/reduced-motion, document width stayed 390px; external-open and mode controls were 36px tall and the menu trigger 40px. The directory URL's Open in browser link was present in both layouts. This uses synthetic HTTP responses, not another real-device test or screenshot acceptance.
- Validation: 256 Web tests / 68 files, TypeScript, production build and `go test ./...` passed. Vite production build completed before Go tests. Temporary HTTP harness files and dev process were removed after checks.
