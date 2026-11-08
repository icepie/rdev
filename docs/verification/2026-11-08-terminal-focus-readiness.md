# Terminal readiness and focus isolation

Both the standalone terminal and Agent panel terminal now gate keyboard input and resize messages on a connected session. Delayed ready/auth replies only focus the terminal when focus is unclaimed or already within its own controls. Explicit user tab switching keeps its existing behavior.

Panel status changes update the event-handler ref synchronously. Disconnected sockets cannot revive the session through late open/ready/binary messages; deferred Blob decoding also checks the disconnected state before writing.

The panel component regression test exercises input before ready, same-tick input after auth, hidden-session continuity, external composer focus, input/resize after error, late frames, reconnect, and replaced-socket callbacks.

Validation: typecheck passed; all 73 Web test files / 279 tests passed; production build passed; git diff --check passed. This is automated component validation, not real-device or BB pixel-level visual acceptance. User screenshot controls remain unidentified because only image placeholders were available.
