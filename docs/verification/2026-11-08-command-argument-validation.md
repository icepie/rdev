# Reject ignored control-command arguments

OMP's builtin registry checks allowArgs before invoking a command. rdev's
browser previously dispatched /new, /reset and /stop with arbitrary trailing
text, silently ignoring that text while performing the control action.

The shared command descriptor now declares allowArgs. Compact, jobs, bg,
subagent, steer and model opt in; no-argument controls reject extra text with a
localized message, keeping the draft. Whitespace-only suffixes remain valid.
Successful menu or typed-command execution clears previous composer errors.

Validation:
- Dispatcher tests verify multiline trailing text cannot execute new/reset/stop,
  drafts are not cleared, and whitespace-only valid retries execute once.
- Actual composer regression verifies rejected operations emit no frames and
  preserve text while existing parameterized commands continue working.
- TypeScript, 73 web test files / 279 tests, and production build passed.
