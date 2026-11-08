# Correlated background cancellation

Previously the stop button stayed enabled until the next task-state refresh,
and a cancellation had no request-specific receipt. The browser now sends both
a request id and jobId; the server returns job_cancelled only to that connection.
The existing id-as-job-id operation is retained for older browser clients.

The button immediately enters Requesting, rejects duplicate activation, and
marks CancelRequested only after a matching successful receipt. Worker state
remains running until actual exit. Failure, a 15-second timeout, disconnect or
unmount settles and unregisters the request; retries use a new identity. A late
callback cannot settle a subsequent request or affect a different thread.

Guidance and cancellation share the thread-scoped action reply registry. Their
request prefixes and expected response operations remain distinct. Foreground
text streaming is not interrupted by these receipts.

Validation:
- Web regressions cover duplicate activation, wrong-thread/error receipts,
  timeout, disconnect, immediate transport failure, retries, stale callbacks,
  thread switching and cleanup.
- Native WebSocket wire checks cover request/job/thread correlation, rejection
  of another manager's job, acceptance before worker exit, and two-connection
  receipt isolation. Targeted tests passed under the Go race detector.
- TypeScript, 71 web test files / 269 tests, and production build passed.

This changes task cancellation feedback, not foreground stop semantics. No new
real-device visual acceptance was performed in this pass.
