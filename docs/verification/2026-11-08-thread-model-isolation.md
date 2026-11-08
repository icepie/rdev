# Per-thread composer model selection

The server already includes each thread's canonical model in thread_list/auth_ok
(agentThreadInfo.Model). The browser parser discarded it, and AgentPage passed
one shared model and picker callback to every split pane. Selecting in one pane
therefore changed the model sent by the other pane.

The parser now retains model. Each pane resolves its model from its local
next-message choice, then its server thread model, then the legacy fallback.
Local choices are keyed by thread and shared only by panes displaying that same
thread. A completion under the old model does not replace a pending choice.
Deleted threads lose their choice; authentication/device reset restores server
state. Context capacity and supported reasoning options use the pane's selected
model rather than the other pane's selection.

Validation:
- Hook regression uses real parsed thread frames with distinct models, independent
  choices, an old-model completion snapshot, another thread's server model update,
  reset, deletion/recreation and malformed/legacy model fields.
- Existing real composer regression verifies selection appears in the following
  chat payload; all 73 web test files / 276 tests passed.
- TypeScript and production build passed.

Reasoning level and permission mode remain device-wide. No running model turn is
mutated by a local picker action. This supersedes the shared-preference limitation
recorded in 2026-11-08-model-command.md. No new real-device multi-pane visual
acceptance was performed in this pass.
