# Model selector slash entry

Reference: OMP builtin-modes.ts defines model and alias models, with picker
behavior in its TUI and validated selection in the command handler.

rdev's browser now recognizes /model and /models. Without arguments they open
the existing responsive model selector; an exact catalog ID selects the model
through the same callback as that selector. Unknown IDs and empty catalogs
preserve the draft and report a localized error. Attachment validation uses the
shared catalog, so both names reject images without dropping them.

Alias recognition is shared by dispatch and autocomplete. The command palette
contains one canonical entry. The model popup closes on thread, connection or
catalog-size changes. Programmatically opened compact Select content can now
provide its explicit accessible label, rather than depending on a prior pointer
click to populate the trigger label.

Validation: the real composer test exercises invalid selection, alias-based
valid selection, the model ID on the following chat payload, opening the actual
selector, closing on thread change, empty catalogs, and attachment rejection.
TypeScript, all 72 web test files / 275 tests, and production build passed.

Scope: this is the existing shared composer preference for the next message,
not an immediate mutation of a running thread's model. OMP fuzzy IDs, role aliases
and thinking suffixes are not implemented. RPC clients use the existing
get_available_models and set_model methods; the new browser commands are not
advertised as prompt RPC capabilities.
