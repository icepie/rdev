# OMP model selector fields

Reference: OMP `rpc-types.ts` declares `set_model` with `provider` and `modelId`; `rpc-mode.ts` resolves the pair. rdev previously required only `model`, so a native OMP selector failed validation.

The RPC boundary now accepts that pair or the existing rdev `model` reference. It rejects mixed forms, missing/invalid fields, and a slash in the provider identifier, then uses the existing exact catalog resolution and idle-thread model-switch path. Slashes within a model ID are preserved. Command discovery advertises the OMP fields and lists the legacy alternative separately.

Wire regression sends literal OMP frames and checks correlated replies and actual thread model state. Missing providers cannot fall back to a model on another provider, and rejected selectors do not mutate the thread. Existing legacy request coverage remains.

Compatibility boundary: this change covers request selectors; success data remains `{model: "provider/model"}` and available-model metadata remains rdev's catalog. Neither is claimed to be a complete OMP Model object. See docs/agent-workspace.md.

Validation: focused wire-contract test passed; `go test ./...` passed; `go test -race ./internal/server -run 'Test.*RPC' -count=1` passed; `git diff --check` passed. This uses an actual WebSocket connection to the test server, not a deployed upstream model.
