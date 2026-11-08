# Agent file panel offline actions

PanelFiles queued arbitrary outbound frames while offline and flushed them on open, then separately resumed tasks. New offline transfers could therefore execute later without a fresh action, and their start frames could be sent both by the queue and by resumeActive.

Removed the generic outbound queue. Transfer start/end requests now fail the affected task with connection-loss feedback if the socket is not open. Offline directory refresh reports connection loss, and offline auth submission reports an error without retaining the submitted password. Existing active transfers still use the explicit resume flow after reconnect/auth; the download watchdog waits for an open socket instead of turning an existing disconnected download into a new failed action. Disconnect invalidates pending directory replies and updates its notice.

Component regressions verify new offline upload/download tasks are failed and are not replayed; manual retry sends one upload request. Another holds a replacement socket in CONNECTING for 60 seconds and verifies an existing download remains resumable, then starts once with a new wire ID after open. Existing reconnect/upload-ack isolation coverage passes unchanged.

Validation: typecheck passed; all 75 Web files / 290 tests passed; production build and diff whitespace check passed. Controlled socket events were used; this is not a real-network stress test.
