# OMP thinking control concurrency

The thinking-level cycle uses the same device-scoped preference as the browser setter. Its read/modify/write path is now executed while the agent session mutex is held, then persistence and broadcast happen after release. Running threads set `pendingReasoning` and keep their current session; idle threads rebuild for the next turn.

A two-WebSocket race test performs sixteen concurrent cycle requests. It verifies every correlated response, four complete cycles, the final preference, and that a running thread was not rebuilt prematurely. Adaptive model discovery and the ordinary four-level catalog are covered separately.

Validation: full Go tests passed (server 102.629s), focused RPC race tests passed (1.038s), 75 Web files / 290 tests passed, typecheck and production build passed, and diff whitespace check passed.
