# S2 task safety and retention freeze

2026-10-20. **S2_CONTRACT_FROZEN**. Internal SQLite schema v2; external HTTP protocol remains v1.0.0, with no new public fields. This supersedes the initial queued-task/recovery and unbatched retention implementation. G0 owns neutral contracts/docs/lifecycle; G1 owns schema/migration/store; G3 owns HTTP/task scheduling/orchestration. G2 files/interfaces are unchanged. Only synthetic/temporary tests are authorized; no MySQL service preparation or real accounts/model calls.

## Exact neutral additions

```go
const ManagementSchemaVersion = 2
const TaskPreconditionFormatVersion = 1
const MaintenanceInterval = time.Hour
const MaintenanceTimeout = 30 * time.Second
const PruneBatchRows = 100

type TaskPrecondition struct {
    Ordinal int64
    ItemIndex int
    AccountID string
    SnapshotKnown bool
    Missing bool
    Versions
    ClientExpected ExpectedVersions
}
func (p TaskPrecondition) Expected() ExpectedVersions
func (p TaskPrecondition) Check(record AccountRecord) error

type TaskCaptureMode string
const (
    TaskCaptureNone TaskCaptureMode = "none"
    TaskCaptureItems TaskCaptureMode = "items"
    TaskCaptureReloadAuth TaskCaptureMode = "reload-auth"
)
type TaskCaptureInstruction struct { Mode TaskCaptureMode; Expected ExpectedVersions }
type TaskPreconditionSet struct { FormatVersion int; Mode TaskCaptureMode; TargetCount int }
// Added trusted field on Admission:
Capture TaskCaptureInstruction

// TaskWork's fields are all private (json:"-"):
Record TaskRecord
PrivateInput json.RawMessage
Preconditions []TaskPrecondition
PreconditionSet *TaskPreconditionSet

type QueuedTaskRef struct { TaskID string; CreatedAt Timestamp }
type QueuedTaskQuery struct { After *QueuedTaskRef; Limit int }
type QueuedTaskPage struct { Items []QueuedTaskRef; Next *QueuedTaskRef }
func (q QueuedTaskQuery) Validate() error

// Added to TaskStore:
ListTaskPreconditions(context.Context, string, PageQuery) (Page[TaskPrecondition], error)
ListQueuedTasks(context.Context, QueuedTaskQuery) (QueuedTaskPage, error)

// Existing signatures unchanged:
AdmitTask(context.Context, Admission) (AdmittedTask, error)
ClaimTask(context.Context, string) (TaskWork, error)
RecoverTasks(context.Context) ([]string, error)
Prune(context.Context, Timestamp) error
```

No other owner edits neutral types. Constructors are unchanged. G3 sets trusted Admission.Capture from kind/items and copies only explicit client expected into Capture.Expected. G1 validates mode: import=none; existing-target actions=items; reload-auth=reload-auth. Missing capture instruction on new admission is INVALID_REQUEST, not guessed from private JSON. Explicit Expected is allowed for a single target only. No public JSON field is added. G2 already accepts StateCommand.Expected and ReplaceCommand.Expected; no G2 change is required.

## G1 schema and migration responsibility

Open a fresh DB at v2. Migrate existing v1 to v2 transactionally, with no credential file or model operations and no recapture of current versions for old tasks. Reject versions >2. The new table is private, not a public DTO:

```sql
CREATE TABLE task_preconditions (
 task_id TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
 item_index INTEGER NOT NULL CHECK(item_index >= 0),
 account_id TEXT NOT NULL,
 snapshot_known INTEGER NOT NULL,
 missing INTEGER NOT NULL,
 credential_version INTEGER,
 state_version INTEGER,
 client_expected_credential_version INTEGER,
 client_expected_state_version INTEGER,
 PRIMARY KEY(task_id, ordinal),
 UNIQUE(task_id, item_index, account_id)
);
```

Mandatory companion manifest: `task_precondition_sets(task_id TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE, format_version INTEGER NOT NULL, mode TEXT NOT NULL, target_count INTEGER NOT NULL)`. Add constraints enforcing boolean flags and positive safe-integer paired versions when present. Known present snapshots have captured versions; known missing snapshots have NULL captured versions and the requested UUID; unknown legacy snapshots have NULL captured versions and a required explicit client pair.

Add any non-semantic indexes/immutability triggers G1 needs; no FK from account_id to a live account (deletion must not rewrite the historical target set). Rows are immutable while retained. Non-secret snapshots remain until task pruning; terminal/cancelled/interrupted credential-bearing task_inputs are cleared as before. Upload receipts, account/index tombstones and commit evidence remain independent and unpruned.

The DDL migration itself adds the table/indexes and sets PRAGMA user_version=2 in one transaction. Startup RecoverTasks, after account journal recovery, handles legacy queue protection; migration must never fill missing snapshots from today's account state.

G1 additionally persists migration provenance in private `task_legacy_queue(task_id TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE)`. **Only the actual v1→v2 migration transaction** inserts the identities that were queued in v1; fresh v2 databases/tasks never acquire this marker. This is internal storage metadata, not a new neutral/wire field or interface. It contains no account state or credentials and follows its parent task's retention.

Compatibility/operations boundary: v2 is a local SQLite upgrade performed by Open; the old v1 binary does not support a v2 database. Future production activation requires a stopped service and a consistent backup of the management DB (including its SQLite consistency state), account files and commit/file journals as one set. Roll back by restoring that whole consistent set with the corresponding binary. Never lower user_version to pretend v2 is v1 and never overwrite only the DB while leaving newer account files/journals. This round performs migrations only on temporary synthetic databases; no production backup, migration or rollback is authorized or performed.

## Admission and target mapping

G1 AdmitTask checks the existing key/idempotency digest and returns the original queued acceptance FIRST. Same principal/key/body must replay the original task even if its account versions changed, was archived/deleted, or the queue is full. Different content remains IDEMPOTENCY_CONFLICT. Do not alter canonical fingerprint or private body to include server snapshots.

Only a NEW admission captures target IDs and both versions in the same SQLite transaction as task/input/idempotency creation. All target reads use that transaction's snapshot. Admission.Capture.Expected is the original explicit client pair (validate both-or-neither). Persist it separately even if it differs from observed versions: admit the task and report asynchronous VERSION_CONFLICT before any account/model action. Never reject that mismatch at admission, rebase the expectation, or alter the digest. Present targets use SnapshotKnown=true, Missing=false and observed versions. Missing/deleted targets use SnapshotKnown=true, Missing=true, the requested UUID and zero/NULL captured versions; execution reports NOT_FOUND even if that UUID appears later. Existing archived records can be captured for restore/archive; operation-state checks remain at execution.

- import: no existing-target rows; normal 1..100 import items and existing no-replay evidence checks remain.
- replace/test/archive/restore/reload/batch: one immutable row per existing-account item, Ordinal=ItemIndex, same requested AccountID as public item; present rows have positive observed versions, missing rows remain represented explicitly. Existing-item batch/import limits remain 100.
- reload-auth: preserve the existing single aggregate public item (no AccountID). At admission capture **all nondeleted, nonarchived accounts**, including disabled accounts, in stable persistent index order; this preserves the existing eligible-account semantics. Scan account rows in bounded pages within the admission transaction. Rows have Ordinal=0..N-1 and ItemIndex=0. N may exceed 100; the 100-item external batch bound does not truncate this internal target set. N=0 is a valid empty aggregate and completes with zero account/model actions. New accounts after admission are never added; later archive/disable/credential changes are conflicts, never silently excluded/rebased.

ClaimTask returns complete Preconditions and PreconditionSet for ordinary tasks. For reload-auth it returns Preconditions=[] and PreconditionSet.TargetCount=N; G3 pages the immutable table via ListTaskPreconditions (default caller limit50, max200), ordered by Ordinal, with consistent page count/rows. Pagination does not join against current/live accounts or filter changed targets. Check the traversed count/ordinals against the claimed count; missing/corrupt protection interrupts rather than recaptures. Public Task/TaskItem and API schemas have no new snapshot fields.

## Recovery compatibility

RecoverTasks is **startup-only**, once after RecoverAccounts and before worker execution. Preserve original receipts; interrupt running or commit-attempt-evidenced queued work and clear its private credential input without replay, as before.

For unexecuted queued tasks with no precondition rows: import remains safely recoverable under the existing checks. A legacy replace with exactly one known AccountID and a complete positive explicit expected pair in its old private input may recover with SnapshotKnown=false, Missing=false, zero captured Versions and ONLY that original pair in ClientExpected; never consult current versions to invent protection. Existing private shape is taskInput.expected containing expectedCredentialVersion/expectedStateVersion. All other replace/test/archive/restore/reload/batch/reload-auth tasks without complete protection become interrupted; clear task_inputs and keep any original receipt. Corrupt/partial/mismatched rows also interrupt. No silent upgrade of an unprotected legacy queue item.

Both legacy compatibility paths require the `task_legacy_queue` provenance marker, in addition to the existing safety checks. Successful startup protection materializes the allowed manifest/row and consumes that marker in the same transaction. Missing/corrupt v2 protection must never be mistaken for old v1 work: even if a new v2 replace loses every manifest/row and its current versions later reach the original future client expected pair, it remains interrupted and its private input is cleared. No live account/version lookup may manufacture migration provenance. G1 has a dedicated regression for this downgrade attempt; no contract signature changed.

A nonnil TaskWork.PreconditionSet is the mandatory durable capture-complete marker: FormatVersion=1, validated Mode and TargetCount. It distinguishes a valid empty reload set from an unprotected legacy task. Persist it for every new task, including import none/0 and reload-auth N=0. A provenance-marked v1 import may enter startup recovery without it; successful protection creates its none/0 manifest and consumes provenance. No legacy protected task gets a current-state snapshot automatically: only the provenance-marked explicit legacy replace gets an unknown-snapshot row plus set marker. Prune the companion manifest with its task.

## G3 runtime execution and scheduling

Check Missing first (NOT_FOUND without rebinding), then TaskPrecondition.Check against the present record: BOTH observed snapshot and original client expected must match. Even if a later version equals an originally inconsistent expected pair, the admission snapshot still rejects it. TaskPrecondition.Expected returns detached effective final CAS, preferring the original explicit pair. For replace and state mutations pass snapshot.Expected() to G2 commands and retain it through drain/verification/final commit; force does not bypass CAS. For test, compare SnapshotCredentials' UUID/versions to the captured row before calling the verifier; newer credentials/state must fail VERSION_CONFLICT with zero generation on that stale queue item. Do not refresh Expected from execution-time metadata. The verifier/result still binds the tested snapshot and retains current historical-evidence behavior.

Reload-auth enumerates ListTaskPreconditions, never ListAccounts at execution time, and calls Reload with each fixed AccountID + expected pair. Preserve its existing aggregate result shape. Do not call a model implicitly.

ListQueuedTasks is a read-only bounded keyset query, ascending (created_at,task_id), max200. Next is the last returned tuple only when more queued rows exist; Items is [] on empty. No offset full-history scans, task recovery, running interruption or credential input in this query. G3 uses it for ordinary rediscovery and startup queue seeding as appropriate. RecoverTasks is never called as a scheduler retry.

On a failed/uncertain ClaimTask or execution-state write, read GetTask with a bounded cleanup context. Still queued: retain/requeue and retry, never lose it just because the pending hint was removed. Already running but no account/model execution began: mark interrupted with optimistic SaveTask, clear private input, retain receipts; never execute that ambiguous claim. Terminal: skip. Read failure: retain a bounded unresolved scheduling hint and retry readback; do not infer queued or blindly rerun. Ensure a running task known to the current worker is not interrupted by queue polling. Worker rediscovery is bounded/deduplicated; no busy loop and no RecoverTasks at runtime.

## Retention responsibility

G0 starts one process-level maintenance goroutine after successful initial task recovery/start. It immediately calls Prune, then once per hour, with cutoff Clock()-30d and a separate context capped at 30 seconds per run. Generation start/stop/rebind does not recreate it. Error logs contain a static message/catalog code only. Close cancels it and waits for the goroutine and in-flight Prune to finish **before closing Store**.

G0 implementation ownership: `internal/managementmaintenance` is the isolated retention-loop helper and its synthetic unit tests; `app/lifecycle.go`, `management_integration.go` and G0 app tests own its process wiring and close-order acceptance. G3 must not start a second maintenance ticker. The helper imports only standard library plus the neutral contract, so its checks do not require in-progress G1/G3 business packages.

G1 Prune keeps its signature, clamps cutoff to the mandatory 30-day retention, and performs batches of at most PruneBatchRows=100 eligible rows per short transaction with context checks between batches. Include large precondition sets in bounded child-row cleanup rather than an unbounded cascading delete. Running/queued tasks are not pruned. Clear only eligible terminal/cancelled private input; retain identities/index high-water, receipts, credential journals and account evidence. No network/browser/model work in these transactions. Deadline/cancellation leaves already pruned batches durable and the remainder for the next run.

## Acceptance boundary

G1: v1 migration/legacy recovery, immutable admission/replay, newer-state CAS, reload targets >200 and zero targets, queued keyset pages, bounded/cancelled pruning. G3: stale queued all action kinds, no model call on stale test, replay stable, reload fixed pages, transient scheduler failure without runtime recovery. G0: initial/hourly maintenance, timeout/cancellation, one goroutine across generations and join-before-Store.Close. All use synthetic data/temporary SQLite. G0 runs targeted checks until module owners hand off; no uncoordinated full cold build.
