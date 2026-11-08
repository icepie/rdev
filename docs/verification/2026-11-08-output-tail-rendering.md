# Preserve received tool-output tails

Compared BB's apps/app/src/components/thread/timeline/TerminalOutputBlock.tsx:
it memoizes ANSI output and renders the received output instead of imposing a
second head-only text cutoff.

rdev's server already bounds results before EventToolEnd (session.go and
command.go), retaining head/tail content at a 12,000-byte threshold. Background
job snapshots retain a bounded 32-KiB output window. The browser nevertheless
cut both command streams and generic results at 4,000 characters, hiding final
errors or completion messages even when the server had retained them.

Removed that additional browser cutoff for command and generic tool bodies.
Existing bounded scroll areas and server truncation notices remain. Raw ANSI
output now uses the same memoized conversion approach as structured stdout and
stderr; changing unrelated task state does not reparse the snapshot.

Validation:
- Regression renders command stdout/stderr and generic read results over 4,000
  characters and checks final-result/error visibility and retained server notice.
- A DOM test counts ANSI conversion calls across unchanged and changed snapshots,
  and verifies reading above the bottom is not interrupted by new output while
  bottom-following still works.
- TypeScript, 72 web test files / 274 tests, and production build passed.

This restores the complete received window, not content already discarded by
server limits or historical compaction. No new real-device performance benchmark
or screenshot acceptance is claimed.
