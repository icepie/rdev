# BB attachment layout alignment

Source comparison: BB f51e6b0 `plugins/composer/src/AttachmentPreview.tsx` uses 96x64 image thumbnails with an overlaid round remove control, a 28x28 coarse-pointer target, and filename-specific labels. `ConversationAttachments.tsx` uses sent image height 80px, max width 144px, an 8px gap, selected-surface border and raised background.

rdev now applies those rules. Composer filenames remain available through the thumbnail title and accessible label without a separate horizontal filename pill. Remove controls name the attachment and return focus to the composer. The shared ImagePreview accepts sent styling without changing tool image styling or its error/retry flow.

Validation:
- Typecheck, all 73 Web files / 281 tests, production build, and diff whitespace check passed.
- Composer regression removes the first of two images, checks textarea focus and the surviving attachment, then confirms only that image is sent.
- Actual Chromium render of ChatPaneView at 390x844 touch viewport: four long-name attachments produced no horizontal document overflow; pending images measured 96x64, sent image 128x80 (source aspect ratio), and remove targets 28x28.
- With dark class and a 300px pane: clientWidth and scrollWidth both 300; deleting the first attachment left the other three and document.activeElement was TEXTAREA.

Browser validation used a temporary local component fixture, not the deployed server. The fixture initially omitted JobsPanel state/ops and was corrected before measurements. Fixture files were removed, its Vite process stopped, and its browser tab navigated away. No screenshot pixel comparison or claim of complete UI acceptance is made.
