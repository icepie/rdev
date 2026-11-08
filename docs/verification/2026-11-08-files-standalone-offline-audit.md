# Standalone Files offline audit

The Agent panel now rejects new offline operations before they enter a send queue. The standalone Files page still has a general queue because its reconnect flow uses it for request continuity, so this audit checked that its transfer path has an explicit `requireConnectedDevice` gate: a new upload/download waits for a live, authenticated socket and marks the task failed on disconnect instead of replaying a stale transfer frame. Existing transfers retain their persisted resume behavior.

The panel and standalone page therefore differ intentionally: PanelFiles has no replay queue for new actions, while FilesPage keeps its queue for list/preview/request recovery and independently gates transfer attempts. Both paths use task IDs and connection epochs to isolate old acknowledgments.

Validation: panel offline-transfer regression and the existing standalone reconnect suite pass; full Web validation on the preceding change covered 75 files / 290 tests, typecheck and production build. No code change was needed in FilesPage for this audit.
