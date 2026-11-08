# Release inactive HTML preview documents

The shared FilePreview passed source/preview mode to its HTML iframe but ignored
the whole-panel visible flag. Hiding an Agent file panel therefore left scripts
and resource loads alive in the retained document.

HTML previews now unmount when the entire panel is invisible. Their HEAD
preflight is aborted on unmount, and late completion cannot mount a stale frame.
Reopening creates a fresh document. Switching source/preview while the panel is
visible still retains the same iframe, preserving the BB preview-mode behavior.
Uncommitted state inside authored HTML resets when the whole panel is hidden;
this is the explicit resource-release tradeoff.

Validation:
- Component test verifies no fetch/iframe for an initially hidden panel,
  abort on hiding during HEAD, rejection of its late successful response, and
  a new load when visible again.
- Existing mode retention test now also checks hidden-frame removal and reopening.
- Real Chromium isolated fixture used the actual FilePreview and an inline
  sandboxed interval posting a counter every 50ms. Observed counts:
  active 284 -> 290; hidden 290 -> 290 over 400ms; reopened -> 297.
  The iframe was absent while hidden and present after reopening.
- Real DOM identity checks confirmed source and preview modes retained the same
  iframe (sourceRetained=true, previewRetained=true).
- TypeScript, 72 web test files / 275 tests, and production build passed.

The fixture and dev server were removed. This is document lifecycle evidence,
not a full CPU/memory benchmark or real-device network-load measurement.
