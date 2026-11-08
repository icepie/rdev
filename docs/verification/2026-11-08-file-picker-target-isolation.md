# File picker target isolation

Opening the Agent file picker and changing the directory or device before the native file chooser returned could send the selected file to the wrong destination. The picker now records the device and directory at click time. Same-device navigation keeps the original directory target; a device change rejects the selection with a localized message and requires choosing again. Drag-and-drop continues to use the current visible target.

The picker target is cleared on selection or native cancel. Re-selecting a persisted failed upload remains bound to its original task and does not use the ordinary picker target.

Regression coverage opens the picker, refreshes to another directory, verifies the original parent path, switches devices and verifies no upload is sent, then chooses again on the new device. Existing panel transfer and reconnect tests remain green.

Validation: typecheck passed; all 75 Web files / 291 tests passed; production build and diff whitespace check passed. Native file chooser behavior is simulated through the DOM input; no OS-level chooser automation was used.

Additional task-reselect regressions: native cancel clears the failed-task selection so a subsequent ordinary upload uses the current directory and leaves the old task unchanged; switching devices while reselecting a failed task rejects the returned file without sending on either socket or creating a task on the new device. Focused panel suite: 19 tests passed. Full Web suite: 75 files / 293 tests passed; typecheck and production build passed.

Standalone Files comparison: its per-pane task registry intentionally supports transfers to multiple devices, so it preserves the captured original device and directory even after the visible device changes. Its existing component regression checks device-a /current is used after switching to device-b, including password-protected devices. A canceled picker submits nothing. Agent PanelFiles instead replaces its per-device registry/socket on device changes, so it rejects the stale selection and asks for a fresh choice. Both prevent retargeting a selection to the newly visible device. Rechecked standalone files-reconnect and file-request-state suites: 2 files / 10 tests passed.
