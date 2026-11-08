# Agent file-read lifecycle

Agent file previews previously tracked epoch/id but neither timed out nor consumed successful replies. Their pending table was not included in socket-disconnect cleanup. This allowed indefinite loading and repeated application of duplicate responses.

A small file-read request registry now owns replacement, 30-second timeout, one-time reply consumption, and timer cleanup. AgentPage uses it when reading and receiving file_content, clears it on auth/thread/device/cwd context changes and unmount, and settles pending previews as disconnected when replacing or losing a socket. Changing cwd also clears the preview cache so relative paths are fetched in the new working context. Already completed requests are not marked disconnected.

Tests cover parallel paths, superseding an attempt, wrong epoch/id, duplicate reply, expiry, disconnect/reset cleanup, and the retained legacy id-less reply behavior. Modern replies require exact IDs; old id-less servers still cannot distinguish different attempts for the same path within a context, an existing compatibility limitation.

Validation: typecheck passed; all 75 Web files / 288 tests passed; production build and diff whitespace check passed. This verifies the request registry and existing component suite; no new full Agent browser/WebSocket scenario was run for this change.
