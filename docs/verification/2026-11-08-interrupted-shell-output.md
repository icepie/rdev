# Interrupted shell output rendering

Confirmed path: remote runShellReporting returns a cancellation/deadline error followed by `; partial output:` and the stdout/stderr envelope. Agent execution may prepend `error: `. The browser's envelope parser expected the status at the start, so this actual interruption path fell back to raw text and exposed stream tags. Agent history reduction also treated the prefixed envelope as ordinary text and could damage its boundaries.

Both layers now recognize the exact context-canceled/deadline-exceeded prefix and interrupted status. The UI retains the cause, separates stdout/stderr with the existing BB terminal presentation, and includes the cause when copying. Server result truncation and older-history compaction preserve the prefix and transform only the individual streams. Arbitrary text that merely resembles an error envelope is not stripped.

Regression coverage includes canceled and deadline output, optional error prefix, raw-text fallback, Unicode output, truncation budgets, preserved status and stream boundaries, and repeated compaction stability.

Validation: typecheck passed; all 73 Web files / 284 tests passed; production build passed; full Go tests and agent race tests passed; diff whitespace check passed. The initial full Go invocation overlapped Vite's static-output cleanup and failed during embed; it was rerun successfully after the frontend build completed. No real remote cancellation was exercised in this check; the tests use the exact format emitted by runShellReporting.
