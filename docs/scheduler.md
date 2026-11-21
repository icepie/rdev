# Scheduled task persistence and refresh lifecycle

## Durability and recovery

The scheduler has one owner (`schedules/owner.lock`). It commits a version-2
`state.json` manifest (maximum 1 MiB) referencing immutable `record-*.json`
schedule/run records. Only changed records are serialized and synced; retained
run output is not rewritten on every tick. The combined live records and manifest
remain capped at 320 MiB. Each run retains at most 256 KiB of UTF-8 output.

The commit order is: write and fsync changed records, sync their directory, write
and fsync the temporary manifest, rename it over `state.json`, then sync the
directory. Dispatch starts only after this synchronous barrier succeeds. Windows
retains the existing file-sync guarantee but does not support directory fsync.
The scheduler mutex still covers a commit: work is proportional to the bounded
index and changed entities, not all retained output. Initial migration/recovery
can still touch the complete live state; it happens before dispatch starts.

Any write, file sync, rename, directory sync, or cleanup failure latches a storage
fault. No additional work is dispatched until restart. Do not delete the manifest
to clear a fault. Repair storage and restart; the authoritative manifest decides
which records committed, including uncertain rename/sync outcomes. Pending runs
become `interrupted`; dispatching/running runs become `unknown`, never replayed.
Existing misfire/opt-in latest-occurrence policies remain unchanged.

Version-1 monolithic files are loaded and atomically migrated before dispatch.
Missing/malformed/oversized records or invalid references fail startup. Orphan
records from an interrupted commit are removed under the ownership lock before
startup checkpointing. After each commit, replaced/pruned records are removed.
Transient disk headroom may include old and new versions of changed records;
320 MiB is the live committed-state limit, not a hard directory quota. During
initial migration additional space for the old version-1 file is required.

Retention runs at least once per minute even when otherwise idle, and on task
completion/startup: keep at most 1,000 terminal runs globally, 100 per schedule,
and 30 days by scheduled time. Active runs are never pruned. Deleting a schedule
keeps its retained execution history until those limits apply.

## Health contract

`(*Server).SchedulerHealth() SchedulerHealthSnapshot` does **not** initialize the
scheduler. It returns `initialized`, `stopped`, `schedules`, `pending`, `running`
(includes dispatching), optional `persistenceFault`, `lastCommitAt`,
`lastCommitBytes`, and `storedBytes`. Byte counters describe the last successful
manifest commit and live referenced storage, not directory usage. The snapshot
contains no command, prompt, output, or credentials. A storage fault can coexist
with a last successful commit timestamp.

## UI lifecycle

`Tasks.tsx` owns route/list/detail presentation; `TaskForm.tsx` owns editing;
`TasksShared.tsx` owns shared display; `tasks/api.ts` owns transport/log paging;
`tasks/useTaskResource.ts` owns cancellable reads. The list loads schedules and
runs as one snapshot, so switching tabs cannot commit an obsolete tab response.
Refreshes coalesce with in-flight reads and poll three seconds after completion,
not on overlapping intervals. Hidden documents do not poll. Each read has a
10-second deadline; errors/timeouts retain prior rows and retry on the next poll.
Route changes/unmount abort and ignore obsolete responses, even if the transport
ignores cancellation. Follow-off disables periodic run refresh without clearing
visible results. Mutation errors remain visible alongside retained data.

The schedule editor exposes recurring and one-time timing as mutually exclusive
modes. Preview and save normalize the request to exactly one of `cron` or `at`,
so hidden stale values cannot trigger the server's exclusivity validation. Common
intervals fill the normal five-field Cron expression; custom Cron remains
available. Execution defaults match server normalization (`maxWaitSeconds=300`,
`graceSeconds=1800`), and unattended authorization remains an explicit checkbox.

## Reproducible checks (not deployment)

Run after all workspace changes are integrated:

```sh
gofmt -w internal/server/scheduler.go internal/server/scheduler_store.go internal/server/scheduler_test.go internal/server/scheduler_store_test.go
go test ./internal/server -run 'TestSchedule|TestSchedulerHealth' -count=1
go test -race ./internal/server -run 'TestSchedule|TestSchedulerHealth' -count=1
bun run test:web -- web/tests/tasks-resource.test.js
bunx tsc --noEmit
go test ./internal/server -run '^$' -bench BenchmarkSchedulerCheckpointRetainedLogs -benchmem -benchtime=20x -count=3
```

The benchmark seeds 10/100/1,000 full-size retained log records, then changes one
run per commit. Compare `bytes/commit`, `B/op`, allocations and ns/op on the same
filesystem; startup seeding is excluded. The regression also asserts unchanged
record references and a one-record commit budget. No performance measurements
are claimed without executing the benchmark.

Manual smoke on an isolated server/data directory: create a harmless shell task,
run it, inspect its output, restart the server, and confirm completed history is
retained without replay. Under browser network throttling above three seconds,
switch list tabs and run routes rapidly: prior rows should remain during refresh,
old route output must not appear, and at most one resource read should be active.
Block the endpoint for over ten seconds to verify a visible timeout with retained
data and a subsequent retry. Hide/restore the tab and toggle run follow mode.
Never run storage-failure or restart smoke against a production scheduler.
