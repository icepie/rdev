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
