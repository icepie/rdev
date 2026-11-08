# OMP model selection round-trip

Reference: OMP rpc-mode.ts returns model objects from get_available_models, set_model and get_state.model; set_model consumes the object's provider and id as provider/modelId.

Previously rdev advertised canonical ids such as p/m1 in its RPC catalog. Passing that id back as modelId produced p/p/m1. The RPC catalog now uses provider-local ids, with canonicalId retained as an explicit rdev extension. OMP-style set_model replies return the same descriptor directly, and get_state.model uses that descriptor plus a modelRef extension for the canonical string. Browser auth_ok.models is unchanged. Legacy set_model requests using model keep their existing {model: canonical} reply.

The descriptor is an explicit public-data struct: id, provider, canonicalId, name, resolved API, reasoning, and configured context/output limits. Unknown prices and input capabilities are omitted rather than invented; complete OMP catalog metadata compatibility is still outstanding. Consumers of the earlier RPC state string must use modelRef, and consumers of earlier canonical catalog ids must use canonicalId.

The WebSocket wire test selects every advertised model using its provider/id, compares the selection response with the catalog, and checks get_state reports the same model. It includes an id containing a slash and a model-level API override with configured context/output limits. Existing malformed-request and legacy-selector tests remain.

Validation: focused WebSocket round-trip test passed; full `go test ./...` passed (server 102.814s); RPC race suite passed (17.604s); diff whitespace check passed. No real upstream model was needed or invoked for catalog and session-selection validation.
