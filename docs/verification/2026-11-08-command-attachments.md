# Keep control commands out of multimodal chat fallback

The composer recognized all slash commands only when no attachments were
present. Its separate image guard listed compact/bg/subagent, so jobs, reset,
new and stop with images bypassed command dispatch and went to ordinary chat.

Command dispatch and attachment validation now share matchSlashCommand against
the actual browser command catalog. Recognized control commands reject attached
images with the existing localized message and preserve both text and images.
Steer retains its explicit image transport path. Unknown slash text still goes
through ordinary multimodal chat; the change does not reserve arbitrary names.

Validation uses the actual composer:
- compact/bg/subagent/jobs/jobs kill all/reset/new/stop reject attached images
  without sending, clearing text, or removing the attachment;
- unknown /custom text with an image sends the normal chat payload;
- existing image-only and image+text steer checks remain passing;
- dispatcher tests preserve multiline arguments, case-insensitive names,
  retry drafts and running-only controls.

TypeScript, 72 web test files / 275 tests, and production build passed.
