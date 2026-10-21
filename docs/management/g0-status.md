# G0 contract status

2026-10-21 user-requested compatible extension: import items and replacement envelopes accept optional `proxy`; active capabilities declare `accountProxyWrite`; committed upload receipts may include `proxyApplied`. Credential JSON and public account/task projections retain their existing boundaries. See [account proxy writes](account-proxy.md) for submission, versioning, recovery and acceptance scope. No database migration or deployment was performed.

**CONTRACT_FROZEN** — v1.0.0, 2026-10-20 (Asia/Shanghai).

**Current S2 closing state:** dispatcher normal-user `go test -p 2 ./... -count=1`, full vet, Windows CLI build, Linux amd64 CGO-disabled cross-build and canonical fixture build all exit 0. `SourceStable=true`, 322 path+SHA256 inputs unchanged. Maintenance unit package actually PASS 2.792s; its early OS block is historical. G0 source is unchanged; final S2 real HTTP/Manager acceptance is assigned to G6/M4. Evidence: `<shared GOTMPDIR>/s2-closing-result.json` and `s2-all-tests.log`; details in deliveries/G0.md.

**G0 production integration delivered. Final stable-source full Go tests/vet/Windows/Linux builds passed (`GO_SOURCE_STABLE=1`, 302 checked files).** Real module composition and current limits are described below and in deliveries/G0.md. G6's final 10 HTTP/Python and 7 native tests passed; earlier fixture/OS failures below are historical.

This is the initial freeze, not production or joint acceptance. G1/G2/G3 can implement against these signatures now; G4/G5/G6 use the same package. Manager work is permitted in parallel by the dispatcher. G0 has not created or messaged any other chat/agent.

## Files and module ownership

Public package: `github.com/Mag1cFall/AIStudio2API/internal/managementcontract` (`doc.go`, `primitives.go`, `errors.go`, `accounts.go`, `storage.go`, `tasks.go`, `verification.go`, `http.go`). Production imports are standard-library-only. See `contract.md` for mandatory behavior and `openapi.json` for all wire shapes.

G1: `internal/managementstore`. G2: `internal/app/management_accounts*.go` plus assigned native account/auth files. G3: `internal/management`. G4: `internal/managementverify` plus `internal/app/management_verification*.go`. G5: assigned web files. G6: independent integration fixtures and acceptance docs. G0 alone edits this public package, contract/OpenAPI/status, router/app lifecycle/runtime/app roots and final production composition.

## Required construction signatures (future implementations)

```go
// contract aliases internal/managementcontract
// G1: internal/managementstore
func Open(ctx context.Context, options contract.StoreOptions) (contract.Store, error)
// G2: internal/app; process scoped and implements NativeWriteCoordinator too
func newManagementAccountCoordinator(options contract.AccountCoordinatorOptions) (contract.AccountCoordinator, error)
// G3: internal/management
func NewTaskService(options contract.TaskServiceOptions) (contract.TaskService, error)
func NewHTTP(options contract.HTTPOptions) (contract.HTTPRegistrar, error)
// G4: internal/managementverify
func New(options contract.VerifierOptions) (contract.Verifier, error)
// G0: existing API constructor accepts Config.Management contract.HTTPRegistrar
func NewHandler(service aistudio.Service, config api.Config) http.Handler
```

These module constructors are requirements, not stubs added by G0; real implementations are now connected in the integration phase below. NewID/Clock nil options use contract.NewID/time.Now; all test paths are explicit. Task queue default is 100; verifier concurrency default is 1. Initial shared contract has 17 interfaces; S1 adds two more. Exact option and interface declarations follow, copied from formatted source.

## Exact option declarations

`internal/managementcontract/accounts.go`

```go
type AccountCoordinatorOptions struct {
	Store      AccountMetadataStore
	AuthPaths  []string
	JournalDir string
	Clock      Clock
	NewID      IDGenerator
}
```

`internal/managementcontract/http.go`

```go
type HTTPOptions struct {
	Store      Store
	Tasks      TaskService
	Accounts   AccountCoordinator
	Projection Projection
	Clock      Clock
	NewID      IDGenerator
}
```

`internal/managementcontract/http.go`

```go
type ProcessServices struct {
	Store    Store
	Accounts AccountCoordinator
	Tasks    TaskService
	Verifier Verifier
	HTTP     HTTPRegistrar
}
```

`internal/managementcontract/storage.go`

```go
type StoreOptions struct {
	Path  string
	Clock Clock
	NewID IDGenerator
}
```

`internal/managementcontract/tasks.go`

```go
type TaskServiceOptions struct {
	Store      Store
	Accounts   AccountCoordinator
	Verifier   Verifier
	Clock      Clock
	NewID      IDGenerator
	QueueLimit int
}
```

`internal/managementcontract/verification.go`

```go
type VerifierOptions struct {
	Target      VerificationTarget
	Clock       Clock
	NewID       IDGenerator
	Concurrency int
}
```

## Exact public interface declarations

`internal/managementcontract/http.go` (formatted as a one-line declaration in source)

```go
type ConsoleAuthorizer interface{ AuthorizeConsole(*http.Request) error }
type ConsoleAuthorizeFunc func(*http.Request) error
func (f ConsoleAuthorizeFunc) AuthorizeConsole(r *http.Request) error
```

`internal/managementcontract/accounts.go`

```go
type AccountCoordinator interface {
	NativeWriteCoordinator
	Recover(context.Context) error
	BindRuntime(context.Context, AccountRuntime) error
	PrepareCredentials(Credentials) (CredentialMaterial, error)
	Import(context.Context, ImportCommand) (AccountCommitResult, error)
	Replace(context.Context, ReplaceCommand) (AccountCommitResult, error)
	Patch(context.Context, StateCommand) (AccountCommitResult, error)
	Archive(context.Context, StateCommand) (AccountCommitResult, error)
	Restore(context.Context, StateCommand) (AccountCommitResult, error)
	Reload(context.Context, StateCommand) (AccountCommitResult, error)
	Export(context.Context, []string) (AccountExport, error)
	SnapshotCredentials(context.Context, string) (AccountRecord, CredentialMaterial, error)
}
```

`internal/managementcontract/accounts.go`

```go
type NativeWriteCoordinator interface {
	SnapshotNative(context.Context, string) (AccountRecord, error)
	CommitNative(context.Context, CommitIntent, AccountCommitParticipant) (AccountCommitResult, error)
}
```

`internal/managementcontract/accounts.go`

```go
type AccountRuntime interface {
	Epoch() uint64
	Snapshot(context.Context) ([]NativeAccount, error)
	AcquireMutation(ctx context.Context, nativeID string, force bool) (AccountMutationLease, error)
}
```

`internal/managementcontract/accounts.go`

```go
type AccountMutationLease interface {
	Epoch() uint64
	Publish(context.Context, AccountRecord) error
	Release() error
}
```

`internal/managementcontract/http.go`

```go
type HTTPRegistrar interface {
	Handler() http.Handler
	KeyHandler(ConsoleAuthorizer) http.Handler
}
```

`internal/managementcontract/http.go`

```go
type Projection interface {
	Status(context.Context) (SystemStatus, error)
	Readiness(context.Context) (Readiness, error)
	Capabilities(context.Context) (Capabilities, error)
	Settings(context.Context) (SettingsSnapshot, error)
	PatchSettings(context.Context, SettingsPatch) (SettingsUpdated, error)
	Diagnostics(context.Context, PageQuery) (Diagnostics, error)
	Usage(context.Context, PageQuery) (Page[UsageRecord], error)
}
```

`internal/managementcontract/storage.go`

```go
type AccountCommitParticipant interface {
	Prepare(context.Context, CommitPlan) (participantRef string, err error)
	Apply(context.Context, RecoveryRecord) error
	Rollback(context.Context, RecoveryRecord) error
	Finalize(context.Context, RecoveryRecord) error
}
```

`internal/managementcontract/storage.go`

```go
type AccountMetadataStore interface {
	ListAccounts(context.Context, PageQuery) (Page[Account], error)
	GetAccount(context.Context, string) (AccountRecord, error)
	FindAccountByNativeID(context.Context, string) (AccountRecord, error)
	ReconcileAccounts(context.Context, []NativeAccount) error
	// CommitAccount serializes allocation and per-account writers; checks both
	// versions before Prepare; no SQLite transaction is open in participant code.
	CommitAccount(context.Context, CommitIntent, AccountCommitParticipant) (AccountCommitResult, error)
	RecoverAccounts(context.Context, AccountCommitParticipant) error
	GetUploadReceipt(context.Context, UploadIdentity) (*UploadReceipt, error)
}
```

`internal/managementcontract/storage.go`

```go
type KeyStore interface {
	AuthenticateKey(ctx context.Context, token string) (Principal, error)
	IsKeyActive(context.Context, string) (bool, error)
	ListKeys(context.Context) (KeyList, error)
	CreateKey(context.Context, KeyCreateRequest) (KeyCreated, error)
	// RevokeKey atomically revokes admission and cancels queued tasks for the
	// key; running work remains attributed and cooperatively observes shutdown.
	RevokeKey(context.Context, string) (KeyRevoked, error)
}
```

`internal/managementcontract/storage.go`

```go
type TaskStore interface {
	// AdmitTask atomically rechecks key liveness and creates task + idempotency
	// record. Same key/content returns original queued acceptance, even if done.
	AdmitTask(context.Context, Admission) (AdmittedTask, error)
	ListTasks(context.Context, PageQuery) (Page[Task], error)
	GetTask(context.Context, string) (TaskRecord, error)
	ClaimTask(context.Context, string) (TaskWork, error)
	// SaveTask uses optimistic revision and legal monotonic transitions. Upload
	// evidence can only be attached from GetUploadReceipt and is immutable.
	SaveTask(ctx context.Context, record TaskRecord, expectedRevision uint64) (TaskRecord, error)
	RequestTaskCancellation(context.Context, string) (TaskRecord, error)
	CancelQueuedForKey(context.Context, string) error
	// RecoverTasks attaches durable receipts, interrupts running/committed queued
	// work, clears interrupted inputs, returns only safe uncommitted queued IDs.
	// Call only AFTER RecoverAccounts and before starting the worker.
	RecoverTasks(context.Context) ([]string, error)
	Prune(context.Context, Timestamp) error
}
```

`internal/managementcontract/storage.go`

```go
type AuditStore interface {
	AppendAudit(context.Context, AuditEntry) error
	ListAudit(context.Context, PageQuery) (Page[AuditEntry], error)
}
```

`internal/managementcontract/storage.go`

```go
type Store interface {
	AccountMetadataStore
	KeyStore
	TaskStore
	AuditStore
	Close() error
}
```

`internal/managementcontract/tasks.go`

```go
type TaskService interface {
	// Start is process-scoped and follows account recovery. It calls RecoverTasks
	// exactly once before workers start; never called again by StartService.
	// Close cooperatively stops work without undoing commits.
	Start(context.Context) error
	Submit(context.Context, Admission) (TaskAccepted, error)
	Cancel(context.Context, string, Principal) (Task, error)
	CancelForKey(context.Context, string) error
	Close(context.Context) error
}
```

`internal/managementcontract/verification.go`

```go
type Verifier interface {
	Verify(context.Context, VerificationInput, ProgressFunc) (VerificationOutput, error)
	Close(context.Context) error
}
```

`internal/managementcontract/verification.go`

```go
type VerificationTarget interface {
	Open(context.Context, VerificationInput) (VerificationSession, error)
}
```

`internal/managementcontract/verification.go`

```go
type VerificationSession interface {
	Run(context.Context, VerificationProbe, ProgressFunc) (VerificationObservation, error)
	Close() error
}
```

## Frozen decisions

- ProcessServices lives on runtimeManager, survives all generation starts/stops, is recovered/started/closed only at process scope. Production roots still need final assembly.
- AccountRuntime.AcquireMutation returns a releasable lease with a pinned epoch; new account requests remain blocked through CommitAccount and Publish, including failures. BindRuntime serializes against all outstanding leases. Do not reacquire manager.mu under coordinator locks.
- Persistent UUID/non-reused index/tombstones and credential digest reconcile are required. All native credential writers participate; authGeneration is not a persistent management version.
- Both CAS versions are captured before network/verification and checked at commit. PATCH.Enabled is *bool so absent/null can be rejected.
- Immutable receipts are written with final account metadata, indexed by task/item/kind, preserved across later states and recovery. No readback-derived synthetic receipts.
- Empty import items returns INVALID_REQUEST before missing idempotency. English error catalog matches 21 Manager codes exactly; console-only extension is separate. UTC exactly three milliseconds; req_UUIDv4, matching header, exact no-store.
- PlaygroundVerificationResult is its own closed schema with required actualChannel, versions and request ID. The original closed VerificationResult is not extended in place.
- Readonly/operator/admin scopes match OpenAPI, including audit:read on readonly and excluding it on operator. Real console password sessions are required for keys, including when native login is disabled.
- Settings are safe Go projections, settingsWrite:false in phase one; unsupported writes return INVALID_STATE. Ordinary model routing/retries remain unchanged.

## Checks and exact commands

PowerShell environment for every Go command:

```powershell
$env:GOCACHE='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-build'
$env:GOTMPDIR='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp'
```

- `go test -p 2 ./internal/managementcontract -count=1 -v`: compiled; executable launch blocked by Windows Application Control. No test assertion executed, no pass claim. The sandbox attempt first failed because its virtual Temp path was absent; the normal same-user permission flow reached the separate Application Control block.
- `go test -p 2 -c -o C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g0-contract.test.exe ./internal/managementcontract`: PASS (compile only; no execution).
- `go test -p 2 -c -o C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp ./internal/managementcontract ./internal/api ./internal/app`: PASS (all three test packages compiled; no execution).
- `go vet -p 2 ./internal/managementcontract ./internal/api ./internal/app`: PASS.
- Existing installed Ajv 6: 64 OpenAPI component schemas compiled, 11 positive/negative fixture checks and 44 exact-message envelope checks PASS. dependentRequired was translated to equivalent draft-07 dependencies for that existing validator; this was not a full OAS runtime server test.
- Python AST-only read of Manager REMOTE_ERRORS: all 21 status/message pairs match OpenAPI exactly; only extra code is CONSOLE_PASSWORD_REQUIRED. PASS.
- Internal OpenAPI refs resolve; `git diff --check`: PASS.

Suggested module checks after implementation: `go test -p 2 ./internal/managementstore`, `go test -p 2 ./internal/app -run Management`, `go test -p 2 ./internal/management`, `go test -p 2 ./internal/managementverify`. G0 registration checks: `go test -p 2 ./internal/api -run Management` and `go test -p 2 ./internal/app -run ManagementRoot`. Respect the current Application Control restriction; do not bypass it.

## Remaining production assembly

Integration phase: `app/management_integration.go` now wires real G1/G2/G3/G4 constructors, recovers before production pool creation, installs native hooks/epoch, binds the initial generation, starts tasks once, supplies real isolated target configuration and safe Projection, and sets Config.Management. `management_runtime.go` holds full mutation leases through publication and supplies S1 native no-self-drain publication. Process Close shuts down tasks/verifier/generation/store once. Lifecycle delegate calls use short manager snapshots, avoiding a manager lock around coordinator work. Generation rebuild reuses the same process services; auth source path changes require a process restart rather than rebinding the live coordinator silently.

`requestdb/management.go` provides a safe SQL count+offset page in one read transaction; it never selects account labels, errors, attempts, paths or bodies. Historical labels cannot prove management identity, so accountId/index remain null; unsafe/non-alias model text is null. Readiness succeeds with ready:false before a generation/service exists. Settings writes remain disabled. The default Go status activeContextsCount is occupied runtime slots, and browserConnected denotes a known ready warmed Camoufox worker (false for Go WAA).

Latest checks: `go vet -p 2 ./internal/app ./internal/requestdb ./internal/managementcontract` passed; compile-only `go test -p 2 -c -o <shared GOTMPDIR> ./internal/app ./internal/api ./internal/requestdb ./internal/managementcontract` passed. This includes real implementation imports and owned tests; no test executable was launched. G1–G6 continue owner checks; final coordinated builds/regressions and G6 actual synthetic HTTP acceptance remain pending. Actual Google, production, shared MySQL and other-platform acceptance remain unperformed.

Subsequent normal execution on converged sources succeeded: four-package management/schema/SQL/router/app tests passed; four targeted G0 production composition/epoch/publication tests passed; full `go test -p 2 ./...` and full `go vet -p 2 ./...` passed. Windows binary build and Linux amd64 CGO-disabled cross-build passed (no Linux execution). `npm run build` passed and updated actual embedded dist with G5 Key page. Historical executable blocks remain historical, not a permanent claim about this environment. G0 first G6 runner attempt reported 9 errors from control/cleanup HTTP resets; G6 changed fixture/runner afterwards and G0 is rerunning corrected source. See deliveries/G0.md for exact commands and final result.

Current G6 closing evidence: final owner fixture/driver passed 10 real HTTP/Python tests and 7 native/schema/Go-WAA-cold tests (0 failures/errors/skips), SHA256 and logs in deliveries/G6.md. G0's earlier runner errors are historical. A final G0 Go/vet/Windows/Linux sequence completed all checks successfully but source guard detected G2's participant source changing mid-run, so the wrapper returned 2 and stable-source validation remains pending until that edit finishes. No business failure is inferred from that guard result.

Final superseding result: following G2's 11:49:53 participant/test update, full Go tests, full vet, Windows build and Linux amd64 CGO-disabled cross-build all returned exit 0. Complete before/after input path+SHA256 sets matched: `GO_SOURCE_STABLE=1; checked_files=302`, overall wrapper exit 0. `<shared GOTMPDIR>/g0-stable-source-before.json` and `g0-stable-source-after.json` retain evidence. This is the closing G0 result; no production assembly work remains. The earlier guard exit 2 and older HTTP errors above are retained only as history. Real Google/production/shared MySQL/macOS/Qt and Linux execution remain outside G0's performed checks. Phase-one settings writes remain disabled; AuthStates path changes require process restart; historical usage labels are never guessed into account identity.

## Interface change log after freeze

2026-10-20 S2: **S2_CONTRACT_FROZEN**. SQLite v2 private task snapshots + capture-complete manifest; trusted Admission.Capture; explicit Missing; separately preserved client expected with asynchronous VERSION_CONFLICT; private TaskWork.Preconditions/PreconditionSet; fixed/paged reload-auth targets; bounded ListQueuedTasks and startup-only RecoverTasks. Exact declarations, schema and recovery matrix are in [task-safety-v2.md](task-safety-v2.md). G1 owns migration/store, G3 owns submission/scheduler/orchestration, G0 exclusively owns the process initial/hourly 30s Prune timer and join-before-Store.Close. G3 must not create a maintenance timer. G2/public HTTP DTOs unchanged. Earlier closing evidence above belongs to the prior round, not S2 acceptance.

S2 G0 implementation: `internal/managementmaintenance` owns the isolated sequential initial/hourly loop, <=30s child contexts, sanitized catalog-code callback and cancellation/join. The process manager starts it after task recovery/start, retains it across generation rebuild and joins it before Store.Close. Constructor signatures above remain fixed. Contract tests actually passed; maintenance unit tests compiled but their executable was blocked by Windows Application Control in the normal same-user flow. Static vet/compile passed; no policy change or renamed-executable workaround was used. App close-order/rebind checks are being validated as dependencies arrive; exact results are in deliveries/G0.md. Local-v2 upgrade/whole-backup rollback requirements are documented in task-safety-v2.md; current migrations use temporary synthetic DBs only.

S2 G0 closing update: both `TestManagementMaintenanceJoinsBeforeStoreClose` and `TestManagementProductionCompositionEmptyRebind` actually passed in one normal app-targeted run (8.044s). Targeted vet of app/contract/maintenance passed. G0's code work is delivered; all shared signatures remain unchanged. Only maintenance unit execution is NOT_EXECUTED from the OS launch block, plus the dispatcher-owned final all-module acceptance. The exact blocked command/path is in deliveries/G0.md; Go emitted no numeric WinError, so none is inferred. No MySQL service or production migration/backup was prepared.

S2 storage clarification after G1's final handoff: internal `task_legacy_queue` is populated only by actual v1 migration for original queued task IDs. Legacy import/explicit-CAS replace protection requires and consumes that provenance; new v2 work whose manifest/rows are lost cannot downgrade into legacy compatibility, even if live versions later match its old expected pair. This adds no neutral/wire field or signature. G1 reports final 32 top-level tests/all subcases and vet passed, including this regression. G0 made documentation-only updates for this clarification and did not rerun full tests; the dispatcher owns closing all-module acceptance.

S2 superseding Go verification result: dispatcher closing JSON is `{"Tests":0,"Vet":0,"WindowsBuild":0,"LinuxCrossBuild":0,"FixtureBuild":0,"SourceStable":true,"InputCount":322}`. G0 read it and confirmed full-test log lines: maintenance PASS 2.792s, contract PASS 2.214s, app PASS 9.427s. Early maintenance NOT_EXECUTED statements above describe that earlier attempt only; current maintenance assertion result is PASS through the normal full run, without a policy workaround. Source remains steady; G0 only updated delivery/status documentation. Final canonical fixture is `<shared GOTMPDIR>/g6-app.test.exe`; G6/M4 execute the separate real HTTP/Manager combination gate. No further G0 test rerun, source change, MySQL preparation or production action occurred.


2026-10-20 integration supplement S1: `internal/managementcontract/native_lease.go` adds the internal native active-lease proof below. G0 runtime adapter must implement NativeMutationRuntime; G2's in-flight native writes use WithNativeLease and call AcquireNativeMutation while holding the coordinator bind read gate through commit/publication/release. This path validates account, active lease and epoch, never waits for its own request to drain, never blocks behind a management drain awaiting that lease, and preserves its active worker/stream. Management replace/state/archive/delete still use AccountRuntime.AcquireMutation with full drain. No wire/OpenAPI change. Dispatcher must relay S1 to G2/G4/G6 before final assembly.

```go
type NativeLeaseProof interface {
    NativeLeaseAccountID() string
    NativeLeaseEpoch() uint64
    NativeLeaseActive() bool
}
func WithNativeLease(ctx context.Context, proof NativeLeaseProof) context.Context
func NativeLeaseFromContext(ctx context.Context) (NativeLeaseProof, bool)
type NativeMutationRuntime interface {
    AcquireNativeMutation(context.Context, NativeLeaseProof) (AccountMutationLease, error)
}
```

Queue-bound clarification (already in initial freeze; no new field/change): Admission.QueueLimit is a trusted positive bound, copied from TaskServiceOptions.QueueLimit (default 100); Store.AdmitTask enforces the bound together with key liveness/idempotency/task creation in its single admission transaction. G1/G3 must not rely on a service-only precheck.

S1 native hook handoff: G2 has now supplied `AccountPool.SetManagementEpoch(uint64)` and AccountLease's NativeLeaseProof methods; G0 installs the nonzero process-wide epoch before publication through a compile-time checked direct call. AccountStore.SetNativeWriteCoordinator(writer,journal) and AccountPool.SetNativeWriteCoordinator(writer,paths,journal) are used as delivered by G2. No temporary missing-dependency adapters remain. No further public shared signature changed after S1. In particular `Projection.Diagnostics(context.Context, PageQuery) (Diagnostics, error)` and `Diagnostics.Accounts Page[AccountRuntimeState]` were already present in the initial g0-status freeze, not a later unlogged change.
