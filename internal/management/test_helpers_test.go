package management

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

const fixtureAccountID = "00000000-0000-4000-8000-000000000001"
const fixtureKeyID = "mkey_00000000-0000-4000-8000-000000000002"
const fixtureRequestID = "req_00000000-0000-4000-8000-000000000003"

func fixtureTime() time.Time { return time.Date(2026, 10, 20, 1, 2, 3, 456000000, time.UTC) }
func fixtureCredentials() c.Credentials {
	return c.Credentials{AccountName: "synthetic@example.test", Cookies: []c.Cookie{{Name: "SAPISID", Value: "synthetic-only", Domain: ".google.com", Path: "/", Expires: -1, SameSite: "None", Secure: true}, {Name: "__Secure-1PAPISID", Value: "synthetic-only", Domain: ".google.com", Path: "/", Expires: -1, SameSite: "None", Secure: true}, {Name: "__Secure-3PAPISID", Value: "synthetic-only", Domain: ".google.com", Path: "/", Expires: -1, SameSite: "None", Secure: true}}, Origins: []c.StorageOrigin{}}
}

// Unused collaborator methods deliberately remain on the nil embedded interface:
// accidentally reaching a real dependency fails the unit test rather than
// returning an empty-success fake implementation.
type memoryStore struct {
	c.Store
	mu               sync.Mutex
	tasks            map[string]c.TaskRecord
	inputs           map[string]json.RawMessage
	identities       map[string]c.Admission
	accepted         map[string]c.TaskAccepted
	receipts         map[c.UploadIdentity]c.UploadReceipt
	accounts         map[string]c.AccountRecord
	preconditions    map[string][]c.TaskPrecondition
	preconditionSets map[string]c.TaskPreconditionSet
	active           bool
	scopes           []c.Scope
	auditErr         bool
	recoveries       int
	admissions       int
	keyCreates       int
	keyRevokes       int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{tasks: map[string]c.TaskRecord{}, inputs: map[string]json.RawMessage{}, identities: map[string]c.Admission{}, accepted: map[string]c.TaskAccepted{}, receipts: map[c.UploadIdentity]c.UploadReceipt{}, accounts: map[string]c.AccountRecord{}, preconditions: map[string][]c.TaskPrecondition{}, preconditionSets: map[string]c.TaskPreconditionSet{}, active: true, scopes: c.Scopes()}
}
func (m *memoryStore) AuthenticateKey(_ context.Context, token string) (c.Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || token != "mgmt_synthetic" {
		return c.Principal{}, c.NewError(c.CodeUnauthorized)
	}
	return c.Principal{KeyID: fixtureKeyID, Scopes: append([]c.Scope{}, m.scopes...)}, nil
}
func (m *memoryStore) IsKeyActive(context.Context, string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, nil
}
func (m *memoryStore) ListKeys(context.Context) (c.KeyList, error) { return c.KeyList{}, nil }
func (m *memoryStore) CreateKey(_ context.Context, req c.KeyCreateRequest) (c.KeyCreated, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyCreates++
	return c.KeyCreated{Key: c.ManagementKey{ID: fixtureKeyID, Name: req.Name, Scopes: req.Scopes, CreatedAt: c.NewTimestamp(fixtureTime())}, Token: "mgmt_synthetic-once"}, nil
}
func (m *memoryStore) RevokeKey(ctx context.Context, id string) (c.KeyRevoked, error) {
	m.mu.Lock()
	m.active = false
	m.keyRevokes++
	m.mu.Unlock()
	_ = m.CancelQueuedForKey(ctx, id)
	return c.KeyRevoked{ID: id, Revoked: true}, nil
}

func cloneRecord(r c.TaskRecord) c.TaskRecord {
	raw, _ := json.Marshal(r.Task)
	var task c.Task
	_ = json.Unmarshal(raw, &task)
	r.Task = task
	return r
}
func (m *memoryStore) AdmitTask(_ context.Context, a c.Admission) (c.AdmittedTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := a.Principal.KeyID + ":" + a.IdempotencyKey
	if old, ok := m.identities[identity]; ok {
		if old.CanonicalDigest != a.CanonicalDigest {
			return c.AdmittedTask{}, c.NewError(c.CodeIdempotencyConflict)
		}
		return c.AdmittedTask{Accepted: m.accepted[identity], Replayed: true}, nil
	}
	if !m.active {
		return c.AdmittedTask{}, c.NewError(c.CodeUnauthorized)
	}
	queued := 0
	for _, r := range m.tasks {
		if r.Status == c.TaskQueued {
			queued++
		}
	}
	if queued >= a.QueueLimit {
		return c.AdmittedTask{}, c.NewError(c.CodeRateLimited)
	}
	id, _ := c.NewID("task")
	preconditions := []c.TaskPrecondition{}
	if a.Kind == c.TaskReloadAuth {
		rows := []c.AccountRecord{}
		for _, account := range m.accounts {
			if !account.Deleted && !account.Archived {
				rows = append(rows, account)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Index < rows[j].Index })
		for i, account := range rows {
			preconditions = append(preconditions, c.TaskPrecondition{Ordinal: int64(i), ItemIndex: 0, AccountID: account.AccountID, SnapshotKnown: true, Versions: account.Versions})
		}
	} else if a.Kind != c.TaskImport {
		for i, item := range a.Items {
			account, ok := m.accounts[item.AccountID]
			p := c.TaskPrecondition{Ordinal: int64(i), ItemIndex: i, AccountID: item.AccountID, SnapshotKnown: true, Missing: !ok || account.Deleted, ClientExpected: a.Capture.Expected}
			if !p.Missing {
				p.Versions = account.Versions
			}
			preconditions = append(preconditions, p)
		}
	}
	now := c.NewTimestamp(fixtureTime())
	r := c.TaskRecord{Task: c.Task{TaskID: id, Kind: a.Kind, Status: c.TaskQueued, CreatedByKeyID: a.Principal.KeyID, CreatedAt: now, UpdatedAt: now, Items: a.Items, Counts: countItems(a.Items)}, Revision: 1}
	m.tasks[id] = cloneRecord(r)
	m.inputs[id] = append([]byte{}, a.PrivateInput...)
	m.preconditions[id] = preconditions
	m.preconditionSets[id] = c.TaskPreconditionSet{FormatVersion: c.TaskPreconditionFormatVersion, Mode: a.Capture.Mode, TargetCount: len(preconditions)}
	m.identities[identity] = a
	m.admissions++
	accepted := c.TaskAccepted{TaskID: id, Status: c.TaskQueued}
	m.accepted[identity] = accepted
	return c.AdmittedTask{Accepted: accepted}, nil
}
func (m *memoryStore) GetTask(_ context.Context, id string) (c.TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.tasks[id]
	if !ok {
		return r, c.NewError(c.CodeNotFound)
	}
	return cloneRecord(r), nil
}
func (m *memoryStore) ClaimTask(_ context.Context, id string) (c.TaskWork, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.tasks[id]
	if !ok || r.Status != c.TaskQueued || r.CancellationRequested || !m.active {
		return c.TaskWork{}, c.NewError(c.CodeInvalidState)
	}
	now := c.NewTimestamp(fixtureTime())
	r.Status = c.TaskRunning
	r.StartedAt = &now
	r.Revision++
	m.tasks[id] = r
	work := c.TaskWork{Record: cloneRecord(r), PrivateInput: append([]byte{}, m.inputs[id]...)}
	if set, ok := m.preconditionSets[id]; ok {
		work.PreconditionSet = &set
	}
	if r.Kind != c.TaskReloadAuth {
		work.Preconditions = append([]c.TaskPrecondition{}, m.preconditions[id]...)
	}
	return work, nil
}

func (m *memoryStore) ListTaskPreconditions(_ context.Context, id string, q c.PageQuery) (c.Page[c.TaskPrecondition], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := append([]c.TaskPrecondition{}, m.preconditions[id]...)
	total := len(items)
	if q.Offset >= total {
		items = []c.TaskPrecondition{}
	} else {
		end := q.Offset + q.Limit
		if end > total {
			end = total
		}
		items = items[q.Offset:end]
	}
	return c.Page[c.TaskPrecondition]{Items: items, Total: total, Offset: q.Offset, Limit: q.Limit}, nil
}

func (m *memoryStore) ListQueuedTasks(_ context.Context, q c.QueuedTaskQuery) (c.QueuedTaskPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := q.Validate(); err != nil {
		return c.QueuedTaskPage{}, err
	}
	items := []c.QueuedTaskRef{}
	for _, r := range m.tasks {
		if r.Status != c.TaskQueued {
			continue
		}
		ref := c.QueuedTaskRef{TaskID: r.TaskID, CreatedAt: r.CreatedAt}
		if q.After != nil && (ref.CreatedAt.Before(q.After.CreatedAt.Time) || ref.CreatedAt.Equal(q.After.CreatedAt.Time) && ref.TaskID <= q.After.TaskID) {
			continue
		}
		items = append(items, ref)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt.Time) {
			return items[i].TaskID < items[j].TaskID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt.Time)
	})
	result := c.QueuedTaskPage{Items: items}
	if len(items) > q.Limit {
		result.Items = items[:q.Limit]
		next := result.Items[len(result.Items)-1]
		result.Next = &next
	}
	return result, nil
}
func (m *memoryStore) SaveTask(_ context.Context, r c.TaskRecord, revision uint64) (c.TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.tasks[r.TaskID]
	if old.Revision != revision {
		return c.TaskRecord{}, c.NewError(c.CodeVersionConflict)
	}
	r.Revision++
	r.CancellationRequested = old.CancellationRequested || r.CancellationRequested
	m.tasks[r.TaskID] = cloneRecord(r)
	if r.Status.Terminal() {
		delete(m.inputs, r.TaskID)
	}
	return cloneRecord(r), nil
}
func (m *memoryStore) RequestTaskCancellation(_ context.Context, id string) (c.TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.tasks[id]
	if !ok {
		return r, c.NewError(c.CodeNotFound)
	}
	if !r.Status.Terminal() {
		r.CancellationRequested = true
		r.Revision++
		if r.Status == c.TaskQueued {
			r.Status = c.TaskCancelled
			now := c.NewTimestamp(fixtureTime())
			r.FinishedAt = &now
			for i := range r.Items {
				r.Items[i].Status = c.TaskCancelled
			}
			r.Counts = countItems(r.Items)
			delete(m.inputs, id)
		}
		m.tasks[id] = r
	}
	return cloneRecord(r), nil
}
func (m *memoryStore) CancelQueuedForKey(ctx context.Context, key string) error {
	m.mu.Lock()
	ids := []string{}
	for id, r := range m.tasks {
		if r.CreatedByKeyID == key && r.Status == c.TaskQueued {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		_, _ = m.RequestTaskCancellation(ctx, id)
	}
	return nil
}
func (m *memoryStore) RecoverTasks(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveries++
	ids := []string{}
	for id, r := range m.tasks {
		if r.Status == c.TaskQueued {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (m *memoryStore) GetUploadReceipt(_ context.Context, identity c.UploadIdentity) (*c.UploadReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.receipts[identity]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (m *memoryStore) AppendAudit(context.Context, c.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.auditErr {
		return errors.New("synthetic private detail must not be public")
	}
	return nil
}
func (m *memoryStore) GetAccount(_ context.Context, id string) (c.AccountRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[id]
	if !ok {
		return a, c.NewError(c.CodeNotFound)
	}
	return a, nil
}
func (m *memoryStore) ListAccounts(_ context.Context, q c.PageQuery) (c.Page[c.Account], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []c.Account{}
	for _, a := range m.accounts {
		items = append(items, a.Account)
	}
	total := len(items)
	if q.Offset >= total {
		items = []c.Account{}
	} else {
		end := q.Offset + q.Limit
		if end > total {
			end = total
		}
		items = items[q.Offset:end]
	}
	return c.Page[c.Account]{Items: items, Total: total, Offset: q.Offset, Limit: q.Limit}, nil
}
func (m *memoryStore) ListTasks(_ context.Context, q c.PageQuery) (c.Page[c.Task], error) {
	return c.Page[c.Task]{Offset: q.Offset, Limit: q.Limit}, nil
}
func (m *memoryStore) ListAudit(_ context.Context, q c.PageQuery) (c.Page[c.AuditEntry], error) {
	return c.Page[c.AuditEntry]{Offset: q.Offset, Limit: q.Limit}, nil
}

type fakeAccounts struct {
	c.AccountCoordinator
	store      *memoryStore
	mu         sync.Mutex
	imports    int
	replaces   int
	patches    int
	commitHook func(context.Context)
	prepareErr error
	reloadIDs  []string
}

func (a *fakeAccounts) PrepareCredentials(credential c.Credentials) (c.CredentialMaterial, error) {
	if a.prepareErr != nil {
		return c.CredentialMaterial{}, a.prepareErr
	}
	raw, _ := json.Marshal(credential)
	return c.CredentialMaterial{NativeID: credential.AccountName, StorageState: raw}, nil
}
func (a *fakeAccounts) Import(ctx context.Context, cmd c.ImportCommand) (c.AccountCommitResult, error) {
	a.mu.Lock()
	a.imports++
	a.mu.Unlock()
	name := cmd.Credentials.AccountName
	id, _ := c.NewID("")
	record := c.AccountRecord{Account: c.Account{AccountID: id, Index: 7, AccountName: &name, AccountState: c.AccountState{Enabled: true}, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}}, NativeID: name}
	a.store.mu.Lock()
	a.store.accounts[id] = record
	receipt := c.UploadReceipt{Status: "committed", AccountID: id, Index: 7, Versions: record.Versions, CommittedAt: c.NewTimestamp(fixtureTime())}
	a.store.receipts[*cmd.Operation.Upload] = receipt
	a.store.mu.Unlock()
	if a.commitHook != nil {
		a.commitHook(ctx)
	}
	return c.AccountCommitResult{Account: record, Upload: &receipt, Published: true}, ctx.Err()
}
func (a *fakeAccounts) SnapshotCredentials(ctx context.Context, id string) (c.AccountRecord, c.CredentialMaterial, error) {
	account, err := a.store.GetAccount(ctx, id)
	material, _ := a.PrepareCredentials(fixtureCredentials())
	return account, material, err
}
func (a *fakeAccounts) Replace(_ context.Context, cmd c.ReplaceCommand) (c.AccountCommitResult, error) {
	a.mu.Lock()
	a.replaces++
	a.mu.Unlock()
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	r := a.store.accounts[cmd.AccountID]
	if *cmd.Expected.Credential != r.CredentialVersion || *cmd.Expected.State != r.StateVersion {
		return c.AccountCommitResult{}, c.NewError(c.CodeVersionConflict)
	}
	r.CredentialVersion++
	if cmd.Enable && !r.Enabled {
		r.Enabled = true
		r.StateVersion++
	}
	a.store.accounts[cmd.AccountID] = r
	receipt := c.UploadReceipt{Status: "committed", AccountID: r.AccountID, Index: r.Index, Versions: r.Versions, CommittedAt: c.NewTimestamp(fixtureTime())}
	a.store.receipts[*cmd.Operation.Upload] = receipt
	return c.AccountCommitResult{Account: r, Upload: &receipt, Published: true}, nil
}
func (a *fakeAccounts) Patch(_ context.Context, cmd c.StateCommand) (c.AccountCommitResult, error) {
	a.mu.Lock()
	a.patches++
	a.mu.Unlock()
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	r := a.store.accounts[cmd.AccountID]
	if cmd.Expected.Credential != nil && (*cmd.Expected.Credential != r.CredentialVersion || *cmd.Expected.State != r.StateVersion) {
		return c.AccountCommitResult{}, c.NewError(c.CodeVersionConflict)
	}
	if r.Enabled != cmd.Enabled {
		r.Enabled = cmd.Enabled
		r.StateVersion++
	}
	a.store.accounts[cmd.AccountID] = r
	return c.AccountCommitResult{Account: r, Published: true}, nil
}

func (a *fakeAccounts) Archive(ctx context.Context, cmd c.StateCommand) (c.AccountCommitResult, error) {
	cmd.Enabled = false
	result, err := a.Patch(ctx, cmd)
	if err != nil {
		return result, err
	}
	a.store.mu.Lock()
	result.Account.Archived = true
	a.store.accounts[cmd.AccountID] = result.Account
	a.store.mu.Unlock()
	return result, nil
}
func (a *fakeAccounts) Restore(ctx context.Context, cmd c.StateCommand) (c.AccountCommitResult, error) {
	cmd.Enabled = false
	result, err := a.Patch(ctx, cmd)
	if err != nil {
		return result, err
	}
	a.store.mu.Lock()
	result.Account.Archived = false
	a.store.accounts[cmd.AccountID] = result.Account
	a.store.mu.Unlock()
	return result, nil
}
func (a *fakeAccounts) Reload(ctx context.Context, cmd c.StateCommand) (c.AccountCommitResult, error) {
	r, err := a.store.GetAccount(ctx, cmd.AccountID)
	if err == nil && cmd.Expected.Credential != nil && (*cmd.Expected.Credential != r.CredentialVersion || *cmd.Expected.State != r.StateVersion) {
		return c.AccountCommitResult{}, c.NewError(c.CodeVersionConflict)
	}
	a.mu.Lock()
	a.reloadIDs = append(a.reloadIDs, cmd.AccountID)
	a.mu.Unlock()
	return c.AccountCommitResult{Account: r, Published: err == nil}, err
}
func (a *fakeAccounts) Export(ctx context.Context, ids []string) (c.AccountExport, error) {
	result := c.AccountExport{Items: []c.ExportItem{}}
	for _, id := range ids {
		account, err := a.store.GetAccount(ctx, id)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, c.ExportItem{AccountID: id, Index: account.Index, Credentials: fixtureCredentials()})
	}
	return result, nil
}

type fakeVerifier struct {
	mu     sync.Mutex
	calls  int
	verify func(context.Context, c.VerificationInput) (c.VerificationOutput, error)
}

func (v *fakeVerifier) Verify(ctx context.Context, in c.VerificationInput, _ c.ProgressFunc) (c.VerificationOutput, error) {
	v.mu.Lock()
	v.calls++
	v.mu.Unlock()
	if v.verify != nil {
		return v.verify(ctx, in)
	}
	status := 200
	stage := "model_verified"
	if in.Mode == c.VerificationConnection {
		stage = "connection_verified"
	}
	return c.VerificationOutput{Result: c.VerificationResult{Success: true, AuthIndex: in.Account.Index, Model: in.Model, RequestID: fixtureRequestID, UpstreamStatus: &status, Stage: stage, Versions: in.Account.Versions, ActualChannel: c.ChannelPlayground}}, nil
}
func (v *fakeVerifier) Close(context.Context) error { return nil }

type fakeProjection struct{ c.Projection }

func (*fakeProjection) Status(context.Context) (c.SystemStatus, error) {
	return c.SystemStatus{ServiceState: "stopped", WAABackend: "go"}, nil
}
func (*fakeProjection) Readiness(context.Context) (c.Readiness, error) {
	return c.Readiness{Ready: false}, nil
}
func (*fakeProjection) Capabilities(context.Context) (c.Capabilities, error) {
	return c.Capabilities{Backend: "aistudio2api", ProtocolVersion: c.ProtocolVersion}, nil
}
func (*fakeProjection) Settings(context.Context) (c.SettingsSnapshot, error) {
	return c.SettingsSnapshot{}, nil
}
func (*fakeProjection) Diagnostics(_ context.Context, q c.PageQuery) (c.Diagnostics, error) {
	return c.Diagnostics{Accounts: c.Page[c.AccountRuntimeState]{Offset: q.Offset, Limit: q.Limit}}, nil
}
func (*fakeProjection) Usage(_ context.Context, q c.PageQuery) (c.Page[c.UsageRecord], error) {
	return c.Page[c.UsageRecord]{Offset: q.Offset, Limit: q.Limit}, nil
}

type captureTasks struct {
	c.TaskService
	calls []c.Admission
	err   error
}

func (s *captureTasks) Submit(_ context.Context, a c.Admission) (c.TaskAccepted, error) {
	s.calls = append(s.calls, a)
	return c.TaskAccepted{TaskID: "task_00000000-0000-4000-8000-000000000004", Status: c.TaskQueued}, s.err
}
func (s *captureTasks) CancelForKey(context.Context, string) error { return nil }
func (s *captureTasks) Cancel(_ context.Context, id string, _ c.Principal) (c.Task, error) {
	return c.Task{TaskID: id, Kind: c.TaskTest, Status: c.TaskCancelled, CreatedByKeyID: fixtureKeyID, CreatedAt: c.NewTimestamp(fixtureTime()), UpdatedAt: c.NewTimestamp(fixtureTime()), Items: []c.TaskItem{}}, nil
}

func awaitTerminal(t *testing.T, store *memoryStore, id string) c.TaskRecord {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		r, err := store.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Terminal() {
			return r
		}
		select {
		case <-deadline:
			t.Fatalf("task remained %s: %+v", r.Status, r.Counts)
		case <-tick.C:
		}
	}
}
