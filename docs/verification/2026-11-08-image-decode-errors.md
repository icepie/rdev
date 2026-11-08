# Image decode failure recovery

Retained the BB image preview/lightbox layout and added failure handling for
images that reached the browser but cannot be decoded.

- File previews show a localized failure message and refetch action. Refetch
  is unavailable offline. A refreshed image at the same path gets independent
  decode/lightbox state.
- Shared full-screen previews (attachments, conversation images and read results)
  show a retry action, disable pan/zoom while failed, and preserve gallery
  navigation. Retry remounts the image rather than changing its data URL.

Validation:
- TypeScript, 71 web test files / 271 tests, and production build passed.
- Component tests cover decoding failure, retry, gallery switching, same-path
  replacement, offline refresh gating, and absence of wheel interception while
  the failed preview is shown.
- Real Chromium at 390x844: invalid PNG data displayed the Chinese failure and
  retry controls. Switching to a generated valid PNG cleared the error and
  produced naturalWidth=240; document scrollWidth did not exceed innerWidth.
  These were DOM/event checks on an isolated fixture using the actual components.
- Screenshot capture timed out. No screenshot-based visual acceptance is claimed
  for this pass, and the temporary fixture/dev server were removed.
