package managementcontract

import (
	"context"
	"encoding/json"
	"fmt"
)

type Versions struct {
	CredentialVersion uint64 `json:"credentialVersion"`
	StateVersion      uint64 `json:"stateVersion"`
}

// ExpectedVersions is optional only for legacy/native state operations. Both
// pointers must be supplied together; zero is never a valid supplied version.
type ExpectedVersions struct {
	Credential *uint64 `json:"expectedCredentialVersion,omitempty"`
	State      *uint64 `json:"expectedStateVersion,omitempty"`
}

func (v ExpectedVersions) Validate(required bool) error {
	if v.Credential == nil && v.State == nil && !required {
		return nil
	}
	if v.Credential == nil || v.State == nil || *v.Credential < 1 || *v.State < 1 ||
		*v.Credential > MaxSafeInteger || *v.State > MaxSafeInteger {
		return NewError(CodeInvalidRequest)
	}
	return nil
}

type AccountState struct {
	Enabled        bool       `json:"enabled"`
	Archived       bool       `json:"archived"`
	Expired        bool       `json:"expired"`
	DisabledReason *string    `json:"disabledReason"`
	DisabledStatus *int       `json:"disabledStatus"`
	DisabledAt     *Timestamp `json:"disabledAt"`
}

// Account is a credential-free public projection. All state and version fields
// are always present, including explicit nulls for optional disabled metadata.
type Account struct {
	AccountID   string  `json:"accountId"`
	Index       int64   `json:"index"`
	AccountName *string `json:"accountName"`
	AccountState
	Versions
}

// AccountRecord maps persistent public identity to native normalized email.
// Native IDs, credential paths and file state never belong to a public DTO.
type AccountRecord struct {
	Account
	NativeID         string   `json:"-"`
	Deleted          bool     `json:"-"`
	CredentialDigest [32]byte `json:"-"`
}

type Cookie struct {
	Name         string  `json:"name"`
	Value        string  `json:"value"`
	Domain       string  `json:"domain"`
	Path         string  `json:"path"`
	Expires      float64 `json:"expires"`
	HTTPOnly     bool    `json:"httpOnly"`
	Secure       bool    `json:"secure"`
	SameSite     string  `json:"sameSite"`
	PartitionKey string  `json:"partitionKey,omitempty"`
}
type StorageItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type StorageOrigin struct {
	Origin       string        `json:"origin"`
	LocalStorage []StorageItem `json:"localStorage"`
}

// Credentials is secret: only import/replace input and accounts:export may use
// this type. External auth extensions and management control flags are rejected.
type Credentials struct {
	AccountName string          `json:"accountName,omitempty"`
	Cookies     []Cookie        `json:"cookies"`
	Origins     []StorageOrigin `json:"origins"`
}

// CredentialMaterial is internal full native storage state, including supported
// native extensions. It deliberately cannot be serialized into a public result.
// Callers must not log it or serialize its StorageState field separately.
type CredentialMaterial struct {
	StorageState json.RawMessage
	NativeID     string
}

func (CredentialMaterial) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("credential material cannot be serialized")
}
func (CredentialMaterial) String() string   { return "[credential material redacted]" }
func (CredentialMaterial) GoString() string { return "[credential material redacted]" }

type ImportItem struct {
	ClientRef   string          `json:"clientRef"`
	Credentials json.RawMessage `json:"credentials"`
}
type ImportRequest struct {
	Items  []ImportItem `json:"items"`
	Model  string       `json:"model,omitempty"`
	Verify *bool        `json:"verify,omitempty"`
}
type ReplaceRequest struct {
	Credentials json.RawMessage `json:"credentials"`
	ExpectedVersions
	Verify *bool `json:"verify,omitempty"`
}
type AccountPatch struct {
	Enabled *bool `json:"enabled"`
	Force   bool  `json:"force,omitempty"`
	ExpectedVersions
}
type ForceRequest struct {
	Force bool `json:"force,omitempty"`
}
type BatchRequest struct {
	Action     string   `json:"action"`
	AccountIDs []string `json:"accountIds"`
	Force      bool     `json:"force,omitempty"`
}
type ExportRequest struct {
	AccountIDs []string `json:"accountIds"`
}
type ExportItem struct {
	AccountID   string      `json:"accountId"`
	Index       int64       `json:"index"`
	Credentials Credentials `json:"credentials"`
}
type AccountExport struct {
	Items []ExportItem `json:"items"`
}

type UploadIdentity struct {
	TaskID    string
	ItemIndex int
	Kind      TaskKind
}
type UploadReceipt struct {
	Status    string `json:"status"`
	AccountID string `json:"accountId"`
	Index     int64  `json:"index"`
	Versions
	CommittedAt Timestamp `json:"committedAt"`
}
type UploadOnlyResult struct {
	Success bool   `json:"success"`
	Stage   string `json:"stage"`
	Versions
}

// OperationContext carries trusted server attribution, never credential JSON.
type OperationContext struct {
	RequestID      string
	CreatedByKeyID string
	Upload         *UploadIdentity
	Progress       ProgressFunc
}
type ImportCommand struct {
	Operation   OperationContext
	Credentials Credentials
}
type ReplaceCommand struct {
	Operation   OperationContext
	AccountID   string
	Credentials Credentials
	Expected    ExpectedVersions
	// Enable is true for the versioned envelope; false preserves legacy flags.
	Enable bool
	// Candidate is the verifier-refreshed candidate for verify:true replacement.
	Candidate *CredentialMaterial
}
type StateCommand struct {
	Operation OperationContext
	AccountID string
	Expected  ExpectedVersions
	Force     bool
	Enabled   bool
}
type AccountCommitResult struct {
	Account AccountRecord
	Upload  *UploadReceipt
	Changed bool
	// Published is false when durable commit succeeded but runtime refresh failed.
	// This must not cause another credential write or a fabricated missing receipt.
	Published bool
}

// NativeAccount is a safe legacy reconciliation seed. Reconcile allocates stable
// identity/version metadata without rewriting existing credential files.
type NativeAccount struct {
	NativeID         string
	AccountName      *string
	State            AccountState
	CredentialDigest [32]byte
}

// AccountCoordinator is process-scoped. All native UI, pairing, relogin, Chrome
// import, automatic cookie refresh and management writes share its commit gate.
// Verification/network/drain work is outside the short commit gate and DB txn;
// both versions are checked again at commit.
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

// AccountCoordinatorOptions is passed to app.newManagementAccountCoordinator.
// AuthPaths and JournalDir are explicit testable paths, never implicit globals.
type AccountCoordinatorOptions struct {
	Store      AccountMetadataStore
	AuthPaths  []string
	JournalDir string
	Clock      Clock
	NewID      IDGenerator
}

// NativeWriteCoordinator is installed on the native AccountStore and AccountPool
// by the composition root. Native refresh snapshots capture BOTH versions before
// doing network work, then submit through the same durable CommitAccount gate.
// It is not the native in-memory authGeneration counter.
type NativeWriteCoordinator interface {
	SnapshotNative(context.Context, string) (AccountRecord, error)
	CommitNative(context.Context, CommitIntent, AccountCommitParticipant) (AccountCommitResult, error)
}

// AccountRuntime is the current generation's publication adapter. BindRuntime
// runs after Recover and before admitting production requests. AcquireMutation
// drains within 60s, then keeps new requests blocked until Release; force:false
// never interrupts active generation. Epoch is nonzero and never reused within
// a process. BindRuntime is serialized against all outstanding mutation leases.
type AccountRuntime interface {
	Epoch() uint64
	Snapshot(context.Context) ([]NativeAccount, error)
	AcquireMutation(ctx context.Context, nativeID string, force bool) (AccountMutationLease, error)
}

// AccountMutationLease pins the exact generation from drain through publication.
// Release is idempotent and mandatory on success, cancellation and every failure.
// Publish is local pool/header/worker invalidation, never network or model work.
// If a durable commit cannot be published, keep that account unavailable until
// successful reconciliation; releasing the lease must not expose stale files.
type AccountMutationLease interface {
	Epoch() uint64
	Publish(context.Context, AccountRecord) error
	Release() error
}
