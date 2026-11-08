# Subagent message selector validation

The RPC message reader previously matched subagentId OR sessionFile. Conflicting selectors could select whichever matching job appeared first. The handler now rejects conflicting identities before reading jobs. A JSON null cursor previously unmarshaled into Go's zero integer; null is now rejected along with negative, fractional, string, and out-of-range cursors.

Wire regression verifies invalid selectors return invalid_request without data, and that URI-only and matching ID/URI requests still succeed. Discovery now documents the accepted message selector fields. Removed an obsolete workspace-doc statement that this already implemented command was unsupported.
