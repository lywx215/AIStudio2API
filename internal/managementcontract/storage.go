package managementcontract

import (
	"context"
	"encoding/json"
)

// StoreOptions is passed to managementstore.Open. Path is an explicit SQLite
// filename; no implementation reads .env, real auth paths or global config.
type StoreOptions struct {
	Path  string
	Clock Clock
	NewID IDGenerator
}

type MutationKind string

const (
	MutationImport  MutationKind = "import"
	MutationReplace MutationKind = "replace"
	MutationState   MutationKind = "state"
	MutationArchive MutationKind = "archive"
	MutationRestore MutationKind = "restore"
	MutationRefresh MutationKind = "refresh"
	MutationDelete  MutationKind = "delete"
)

// CommitIntent never contains credential bytes. State nil preserves the latest
// flags. Import allocates an enabled, unarchived record. Replace increments the
// credential version; stateVersion increments only for real state changes.
// Refresh increments credentialVersion only when credential bytes changed.
type CommitIntent struct {
	OperationID       string
	Kind              MutationKind
	AccountID         string
	NativeID          string
	AccountName       *string
	Expected          ExpectedVersions
	State             *AccountState
	CredentialChanged bool
	CredentialDigest  [32]byte
	Upload            *UploadIdentity
}

type CommitPlan struct {
	Intent CommitIntent
	Before *AccountRecord
	After  AccountRecord
	// Proposed receipt is made durable atomically with After, never before Apply.
	Receipt *UploadReceipt
}
type CommitPhase string

const (
	CommitPlanned   CommitPhase = "planned"
	CommitPrepared  CommitPhase = "prepared"
	CommitApplied   CommitPhase = "applied"
	CommitCommitted CommitPhase = "committed"
)

type RecoveryRecord struct {
	Plan           CommitPlan
	Phase          CommitPhase
	ParticipantRef string
}

// AccountCommitParticipant is owned by G2. Each method is bounded local file
// work; no network/model/browser wait is permitted. Prepare durably stages and
// backs up files in its own journal keyed by OperationID before returning a ref.
// Apply/Rollback/Finalize must be idempotent across process crashes. Rollback
// also accepts planned records whose Prepare crashed before returning a ref.
type AccountCommitParticipant interface {
	Prepare(context.Context, CommitPlan) (participantRef string, err error)
	Apply(context.Context, RecoveryRecord) error
	Rollback(context.Context, RecoveryRecord) error
	Finalize(context.Context, RecoveryRecord) error
}

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

type ManagementKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scopes    []Scope    `json:"scopes"`
	CreatedAt Timestamp  `json:"createdAt"`
	ExpiresAt *Timestamp `json:"expiresAt"`
	RevokedAt *Timestamp `json:"revokedAt"`
}
type KeyCreateRequest struct {
	Name      string     `json:"name"`
	Scopes    []Scope    `json:"scopes"`
	ExpiresAt *Timestamp `json:"expiresAt,omitempty"`
}
type KeyCreated struct {
	Key   ManagementKey `json:"key"`
	Token string        `json:"token"`
}
type KeyRevoked struct {
	ID      string `json:"id"`
	Revoked bool   `json:"revoked"`
}
type KeyList struct {
	Items []ManagementKey `json:"items"`
}
type Principal struct {
	KeyID  string
	Scopes []Scope
}

func (p Principal) Has(scope Scope) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type KeyStore interface {
	AuthenticateKey(ctx context.Context, token string) (Principal, error)
	IsKeyActive(context.Context, string) (bool, error)
	ListKeys(context.Context) (KeyList, error)
	CreateKey(context.Context, KeyCreateRequest) (KeyCreated, error)
	// RevokeKey atomically revokes admission and cancels queued tasks for the
	// key; running work remains attributed and cooperatively observes shutdown.
	RevokeKey(context.Context, string) (KeyRevoked, error)
}

// Admission is private to the service/store boundary. CanonicalDigest is SHA256
// of method + full path + canonical normalized JSON. PrivateInput is stored
// separately from public Task and must be erased when no longer executable.
// It must never appear in logs, audit, task reads or error strings.
type Admission struct {
	Principal       Principal
	RequestID       string
	IdempotencyKey  string
	CanonicalDigest [32]byte
	Kind            TaskKind
	Items           []TaskItem
	PrivateInput    json.RawMessage
	// Positive queue bound supplied by TaskService, enforced atomically by Store.
	QueueLimit int
	// Trusted capture instruction; never added to the body fingerprint.
	Capture TaskCaptureInstruction
}
type AdmittedTask struct {
	Accepted TaskAccepted
	Replayed bool
}
type TaskRecord struct {
	Task
	Revision              uint64 `json:"-"`
	CancellationRequested bool   `json:"-"`
}
type TaskWork struct {
	Record       TaskRecord      `json:"-"`
	PrivateInput json.RawMessage `json:"-"`
	// Complete for normal tasks (<=100 targets); empty for reload-auth, whose
	// fixed target set is read with ListTaskPreconditions in bounded pages.
	// PreconditionSet is the durable capture-complete marker, including N=0.
	Preconditions   []TaskPrecondition   `json:"-"`
	PreconditionSet *TaskPreconditionSet `json:"-"`
}

type TaskStore interface {
	// AdmitTask atomically rechecks key liveness and creates task + idempotency
	// record. Same key/content returns original queued acceptance, even if done.
	AdmitTask(context.Context, Admission) (AdmittedTask, error)
	ListTasks(context.Context, PageQuery) (Page[Task], error)
	GetTask(context.Context, string) (TaskRecord, error)
	ClaimTask(context.Context, string) (TaskWork, error)
	// Private fixed admission targets; reload-auth never rescans live accounts.
	ListTaskPreconditions(context.Context, string, PageQuery) (Page[TaskPrecondition], error)
	// Runtime scheduling only: bounded keyset scan, never recovery/interrupts.
	ListQueuedTasks(context.Context, QueuedTaskQuery) (QueuedTaskPage, error)
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

type AuditEntry struct {
	AuditID        string    `json:"auditId"`
	RequestID      string    `json:"requestId"`
	CreatedByKeyID string    `json:"createdByKeyId"`
	Action         string    `json:"action"`
	Timestamp      Timestamp `json:"timestamp"`
	Outcome        string    `json:"outcome"`
	AccountID      string    `json:"accountId,omitempty"`
	TaskID         string    `json:"taskId,omitempty"`
	Error          *Error    `json:"error,omitempty"`
}
type AuditStore interface {
	AppendAudit(context.Context, AuditEntry) error
	ListAudit(context.Context, PageQuery) (Page[AuditEntry], error)
}

type Store interface {
	AccountMetadataStore
	KeyStore
	TaskStore
	AuditStore
	Close() error
}
