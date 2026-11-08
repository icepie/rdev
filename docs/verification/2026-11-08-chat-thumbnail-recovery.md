# Chat thumbnail error recovery

Compared BB reference f51e6b0 `ConversationAttachments.tsx` attachment chrome and `TimelineRowDetails.tsx` image-error fallback. rdev's shared ImagePreview retained BB recessed surface/border/rounded attachment styling but previously left broken image elements in composer attachments, sent messages, and image tool results.

The shared thumbnail now replaces decode failures with an image placeholder and retry action, using existing translated image-error text. Retrying remounts the image without opening the lightbox; replacing the trigger image clears the previous error. Existing gallery navigation and full-screen error handling are preserved.

Regression coverage dispatches an image error, verifies the fallback and retry remount, checks that retry does not open a dialog, then replaces the failed source and opens the replacement preview.

Validation: typecheck passed, 73 Web files / 280 tests passed, production build passed, diff whitespace check passed. Browser image decoding and screenshot-specific visual acceptance were not exercised in this check. This fallback is an rdev interaction improvement informed by BB, not a claim that BB provides the same retry behavior.
