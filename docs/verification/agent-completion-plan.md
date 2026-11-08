# Scope: requested items 1, 2, 4, 5, 6

Goal remains active until every item is implemented and verified. Browser/CDP (item 3) is outside this goal.

## 1. BB queue behavior
- [ ] Grouping and merged dispatch
- [ ] Scheduled dispatch
- [ ] Drag reorder
- [ ] Edit holds prevent automatic admission; expire/release safely
- [ ] Failure status and explicit retry
- Existing foundation: identity-based edit/reorder with stale-write rejection; attachments retained.

## 2. Offline conversations
- [ ] Explicit read-only explanations for unavailable device tools
- [x] Clear administrator/device-password authorization states (offline history denial now gives an actionable localized explanation)
- Existing foundation: offline admin history, per-conversation confirmation, tool-free discussion.

## 4. Agent reliability
- [ ] Independent subagent cancellation and retry, including restored jobs
- [ ] Long-task disconnection recovery with honest terminal/interrupted states
- [ ] Conflicts for concurrent session edits
- [ ] Consistent queue/job states and UI

## 5. Device capabilities
- [x] Push online/offline updates
- [ ] Restore sessions on device reconnect where underlying session survives
- [ ] Shared device context across files, terminal, desktop (browser remains future work)
- [ ] Device-scoped permissions and audit records

## 6. Performance
- [ ] Long transcript virtualization with navigation/scroll preservation
- [ ] Lazy loading for large images/screenshots
- [ ] Incremental queue notifications, reconnect snapshot recovery
- [ ] Reduce redundant work across Agent panes

Validation must cover user-visible behavior and concurrency, not merely code presence. Commit and push completed increments without marking the whole goal complete prematurely.

## Increment 1 evidence

- `device_events.go`: authenticated SSE invalidations for register, replacement, and successful disconnect; one pending event per subscriber, initial reconnect invalidation, heartbeat and bounded writes.
- `device_events_test.go`: stale disconnect does not report offline, burst coalescing, subscriber cleanup, authentication and real HTTP stream delivery; targeted race tests passed.
- `devices.tsx` / `device-events.test.js`: coalesces changes during an in-flight fetch without losing the last update; closes streams on unmount; renews stream URL following token refresh; avoids rerenders for identical snapshots.
- Full Web suite: 77 files / 298 tests passed. Typecheck and production build passed.
- Markdown images now use native lazy loading and async decoding; existing conversation image thumbnails already had these attributes. Full large-image memory/rendering verification remains open.
- No claim of complete offline UX, Agent recovery, permissions/audit, or queue parity is made by this increment.
