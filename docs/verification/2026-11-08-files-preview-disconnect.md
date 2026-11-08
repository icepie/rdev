# File preview connection lifecycle

Inspection found that FilesBrowser ended directory/auth requests on socket loss but left preview reads pending until their 30-second timeout. The disconnect path now invalidates the preview request immediately and reports the connection loss for loading previews. Already loaded content stays available. Failed authorization also clears its pending preview ID.

Socket close handlers now reject callbacks from replaced sockets before touching shared queues, preview state, or reconnect timers.

A component-level regression drives the actual FilesBrowser with controlled WebSocket events: begin a read, disconnect, observe the immediate preview error, reconnect and retry, inject an old reply on both sockets and an old close callback, complete the new read, then disconnect again and verify the loaded content remains. FilePreview is stubbed only to observe the state and invoke its existing refresh callback; image/text rendering is covered separately.

Validation: typecheck passed; all 74 Web files / 285 tests passed; production build and diff whitespace check passed. No real-network outage was induced in this validation.
