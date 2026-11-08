# Server-side /jobs command dispatch

The browser already intercepts /jobs, but RPC prompt previously sent it to the
model. The shared conversation dispatcher now invokes the existing jobs tool for:

- /jobs: this thread's job summaries
- /jobs full: job details, including the existing bounded output window
- /jobs kill <id>: request cancellation of a job in this thread
- /jobs kill all: atomically request cancellation of this thread's jobs

Invalid grammar and attached images are rejected before history changes.
The turn is classified as a session command, preventing guidance promotion into
a tool-only turn. Command discovery now advertises jobs in the prompt catalog.

Validation:
- Race-enabled command and RPC wire tests pass.
- A real two-manager test without a model provider verifies output retrieval,
  invalid grammar preserving history, and cancellation staying in one thread.
- Existing tests cover image rejection and Unicode/multiline arguments.

Scope: RPC prompt still requires an idle foreground conversation. The browser
uses get_jobs/cancel_job directly and can operate during a running foreground
turn. Full output means the retained bounded window, not an unlimited log.
