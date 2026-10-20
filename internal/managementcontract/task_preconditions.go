package managementcontract

// Internal SQLite schema version; HTTP ProtocolVersion remains 1.0.0.
const ManagementSchemaVersion = 2
const TaskPreconditionFormatVersion = 1

type TaskCaptureMode string

const (
	TaskCaptureNone       TaskCaptureMode = "none"
	TaskCaptureItems      TaskCaptureMode = "items"
	TaskCaptureReloadAuth TaskCaptureMode = "reload-auth"
)

// Trusted service input, never public JSON or part of the canonical digest.
// Expected is only the original explicit client CAS pair, even when inconsistent
// with the admission snapshot; execution reports that conflict asynchronously.
type TaskCaptureInstruction struct {
	Mode     TaskCaptureMode
	Expected ExpectedVersions
}

// Durable completion manifest. A nonnil format=1 set distinguishes an empty
// captured reload-auth target list from unprotected legacy queued work.
type TaskPreconditionSet struct {
	FormatVersion int
	Mode          TaskCaptureMode
	TargetCount   int
}

// Private immutable target row. Normal actions use Ordinal=ItemIndex. Reload
// uses many ordinals attached to its one public aggregate item (ItemIndex=0).
// Missing preserves a requested UUID absent at admission; never rebind it later.
// SnapshotKnown=false is reserved for a recoverable explicit-CAS legacy replace.
type TaskPrecondition struct {
	Ordinal       int64
	ItemIndex     int
	AccountID     string
	SnapshotKnown bool
	Missing       bool
	Versions
	ClientExpected ExpectedVersions
}

// Expected returns a detached effective G2 CAS pair; explicit client CAS wins.
func (p TaskPrecondition) Expected() ExpectedVersions {
	if p.ClientExpected.Credential != nil && p.ClientExpected.State != nil {
		credential, state := *p.ClientExpected.Credential, *p.ClientExpected.State
		return ExpectedVersions{Credential: &credential, State: &state}
	}
	credential, state := p.CredentialVersion, p.StateVersion
	return ExpectedVersions{Credential: &credential, State: &state}
}

// Check enforces both the observed snapshot and the original client pair before
// any action. A later version cannot rescue an inconsistent admission snapshot.
func (p TaskPrecondition) Check(record AccountRecord) error {
	if p.Missing {
		return NewError(CodeNotFound)
	}
	if p.AccountID != record.AccountID || record.Deleted {
		return NewError(CodeVersionConflict)
	}
	if p.ClientExpected.Validate(false) != nil {
		return NewError(CodeInterrupted)
	}
	if p.SnapshotKnown {
		if p.CredentialVersion < 1 || p.StateVersion < 1 || p.CredentialVersion > MaxSafeInteger || p.StateVersion > MaxSafeInteger || p.Versions != record.Versions {
			return NewError(CodeVersionConflict)
		}
	} else if p.ClientExpected.Credential == nil {
		return NewError(CodeInterrupted)
	}
	if p.ClientExpected.Credential != nil && (*p.ClientExpected.Credential != record.CredentialVersion || *p.ClientExpected.State != record.StateVersion) {
		return NewError(CodeVersionConflict)
	}
	return nil
}

type QueuedTaskRef struct {
	TaskID    string
	CreatedAt Timestamp
}
type QueuedTaskQuery struct {
	After *QueuedTaskRef
	Limit int
}
type QueuedTaskPage struct {
	Items []QueuedTaskRef
	Next  *QueuedTaskRef
}

func (q QueuedTaskQuery) Validate() error {
	if q.Limit < 1 || q.Limit > MaxPageLimit {
		return NewError(CodeInvalidRequest)
	}
	if q.After != nil && (!ValidID(q.After.TaskID) || q.After.CreatedAt.IsZero()) {
		return NewError(CodeInvalidRequest)
	}
	return nil
}
