# OMP thinking-level controls

Reference: OMP rpc-mode.ts exposes `get_available_thinking_levels`, `set_thinking_level`, and `cycle_thinking_level`. rdev already had the setter, but discovery and cycling were missing, so an OMP client could not build a model-aware selector.

The RPC boundary now returns the levels supported by the active thread model and cycles atomically through them. Anthropic adaptive models expose `low`, `medium`, and `high`; other configured models expose `off`, `low`, `medium`, and `high`. Cycling updates the same device-scoped preference/rebuild path as the existing setter and broadcasts the normal reasoning-level event. Thinking control RPCs reject a non-empty thread selector because the setting is device-scoped.

The shared locked update helper avoids a read-modify-write race between cycling and browser/legacy setter operations. The wire contract test checks discovery and the exact cycle response; a model capability test checks adaptive/default/unknown-thread behavior.

Validation: focused server tests, full `internal/server` tests, RPC race suite, and diff check passed. No provider request is made by these controls.
