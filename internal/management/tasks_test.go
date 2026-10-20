package management

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func worker(t *testing.T, store *memoryStore, accounts *fakeAccounts, verifier *fakeVerifier, limit int) *taskService {
	return workerWithStore(t, store, accounts, verifier, limit)
}

func workerWithStore(t *testing.T, store c.Store, accounts *fakeAccounts, verifier *fakeVerifier, limit int) *taskService {
	t.Helper()
	service, err := NewTaskService(c.TaskServiceOptions{Store: store, Accounts: accounts, Verifier: verifier, Clock: fixtureTime, QueueLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	s := service.(*taskService)
	if err = s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func admission(t *testing.T, kind c.TaskKind, key string, input taskInput) c.Admission {
	t.Helper()
	input.RequestID = fixtureRequestID
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	item := c.TaskItem{Status: c.TaskQueued, Stage: "queued", AccountID: fixtureAccountID}
	if kind == c.TaskImport {
		item.AccountID = ""
		item.ClientRef = "local-source"
	}
	return c.Admission{Principal: c.Principal{KeyID: fixtureKeyID, Scopes: c.Scopes()}, RequestID: fixtureRequestID, IdempotencyKey: key, CanonicalDigest: sha256.Sum256(raw), Kind: kind, Items: []c.TaskItem{item}, PrivateInput: raw, Capture: c.TaskCaptureInstruction{Mode: captureMode(kind), Expected: input.Expected}}
}

func seedAccount(store *memoryStore, enabled bool) {
	store.accounts[fixtureAccountID] = c.AccountRecord{Account: c.Account{AccountID: fixtureAccountID, Index: 3, AccountState: c.AccountState{Enabled: enabled}, Versions: c.Versions{CredentialVersion: 5, StateVersion: 6}}, NativeID: "synthetic@example.test"}
}

func TestWorkerUploadOnlyReceiptReplayAndNoGeneration(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{}
	s := worker(t, store, accounts, verifier, 100)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := admission(t, c.TaskImport, "upload", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}})
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskSucceeded || r.Items[0].ClientRef != "local-source" || r.Items[0].Upload == nil || r.Items[0].Result == nil || r.Items[0].Result.Upload == nil || r.Items[0].Index == nil || r.Items[0].AccountID == "" {
		t.Fatalf("incomplete upload: %+v", r)
	}
	if verifier.calls != 0 || accounts.imports != 1 || store.recoveries != 1 {
		t.Fatalf("unexpected calls verify=%d import=%d recover=%d", verifier.calls, accounts.imports, store.recoveries)
	}
	store.mu.Lock()
	live := store.accounts[r.Items[0].AccountID]
	live.CredentialVersion = 9
	live.StateVersion = 11
	store.accounts[live.AccountID] = live
	store.mu.Unlock()
	replayed, err := s.Submit(context.Background(), a)
	if err != nil || replayed != accepted || replayed.Status != c.TaskQueued {
		t.Fatal(replayed, err)
	}
	current, _ := store.GetTask(context.Background(), accepted.TaskID)
	if current.Items[0].Upload.CredentialVersion != 1 {
		t.Fatal("receipt followed live account version")
	}
	a.CanonicalDigest = sha256.Sum256([]byte("different"))
	_, err = s.Submit(context.Background(), a)
	if c.PublicError(err).Code != c.CodeIdempotencyConflict {
		t.Fatal(err)
	}
}

func TestWorkerCancellationAfterCommitRetainsReceiptBeforeVerification(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	entered := make(chan c.VerificationInput, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, in c.VerificationInput) (c.VerificationOutput, error) {
		entered <- in
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, store, accounts, verifier, 100)
	a := admission(t, c.TaskImport, "cancel", taskInput{Verify: true, Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}})
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-entered:
		if in.Account.AccountID == "" || in.Model != c.DefaultModel || in.Candidate == nil {
			t.Fatal("unbound verification")
		}
	case <-time.After(time.Second):
		t.Fatal("verification did not begin")
	}
	before, _ := store.GetTask(context.Background(), accepted.TaskID)
	if before.Items[0].Upload == nil || before.Items[0].Result == nil {
		t.Fatal("verification started before receipt attachment")
	}
	_, err = s.Cancel(context.Background(), accepted.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskCancelled || r.Items[0].Upload == nil || r.Items[0].Result.Upload == nil || r.Items[0].Error.Code != c.CodeCancelled {
		t.Fatalf("cancel lost evidence: %+v", r)
	}
	account, _ := store.GetAccount(context.Background(), r.Items[0].AccountID)
	if !account.Enabled {
		t.Fatal("verification cancellation disabled committed account")
	}
}

func TestWorkerCommitWinsCancelledContext(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{}
	s := worker(t, store, accounts, verifier, 100)
	a := admission(t, c.TaskImport, "commit-wins", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}})
	accounts.commitHook = func(ctx context.Context) {
		s.mu.Lock()
		id := s.activeID
		s.mu.Unlock()
		_, err := s.Cancel(context.Background(), id, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
		if err != nil {
			t.Error(err)
		}
	}
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskCancelled || r.Items[0].Upload == nil || r.Items[0].Result == nil || r.Items[0].Index == nil {
		t.Fatalf("cancelled context hid commit: %+v", r)
	}
}

func TestWorkerShutdownInterruptsAndDoesNotReplayCommittedWork(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	entered := make(chan struct{}, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, store, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskImport, "shutdown", taskInput{Verify: true, Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("verification did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskInterrupted || r.Items[0].Upload == nil {
		t.Fatalf("shutdown lost evidence: %+v", r)
	}
	_, err = s.Submit(context.Background(), admission(t, c.TaskImport, "closed", taskInput{}))
	if c.PublicError(err).Code != c.CodeInvalidState {
		t.Fatal(err)
	}
	_ = worker(t, store, accounts, verifier, 100)
	if accounts.imports != 1 || verifier.calls != 1 {
		t.Fatal("restarted worker replayed committed action")
	}
}

func TestWorkerCandidateDualCASProtectsLaterManualDisable(t *testing.T) {
	store := newMemoryStore()
	seedAccount(store, true)
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{verify: func(_ context.Context, in c.VerificationInput) (c.VerificationOutput, error) {
		if in.Account.AccountID != fixtureAccountID || in.Candidate == nil {
			t.Fatal("candidate not isolated")
		}
		store.mu.Lock()
		live := store.accounts[fixtureAccountID]
		live.Enabled = false
		live.StateVersion++
		store.accounts[fixtureAccountID] = live
		store.mu.Unlock()
		status := 200
		return c.VerificationOutput{Result: c.VerificationResult{Success: true, AuthIndex: in.Account.Index, Model: in.Model, Stage: "model_verified", ActualChannel: c.ChannelPlayground, RequestID: fixtureRequestID, UpstreamStatus: &status, Versions: in.Account.Versions}}, nil
	}}
	s := worker(t, store, accounts, verifier, 100)
	cv, sv := uint64(5), uint64(6)
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskReplace, "replace", taskInput{Verify: true, Model: c.DefaultModel, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskFailed || r.Items[0].Error.Code != c.CodeVersionConflict || r.Items[0].Upload != nil {
		t.Fatalf("stale candidate committed: %+v", r)
	}
	account, _ := store.GetAccount(context.Background(), fixtureAccountID)
	if account.Enabled || account.CredentialVersion != 5 || account.StateVersion != 7 {
		t.Fatal("later state overwritten", account)
	}
}

func TestWorkerUploadOnlyReplacementAndWrongTarget(t *testing.T) {
	store := newMemoryStore()
	seedAccount(store, false)
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{}
	s := worker(t, store, accounts, verifier, 100)
	cv, sv := uint64(5), uint64(6)
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskReplace, "upload-only", taskInput{Model: c.DefaultModel, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	if r.Status != c.TaskSucceeded || r.Items[0].Upload.CredentialVersion != 6 || r.Items[0].Upload.StateVersion != 7 || verifier.calls != 0 {
		t.Fatalf("bad upload-only replace: %+v", r)
	}
	verifier.verify = func(_ context.Context, in c.VerificationInput) (c.VerificationOutput, error) {
		status := 200
		return c.VerificationOutput{Result: c.VerificationResult{Success: true, AuthIndex: in.Account.Index + 1, Model: in.Model, Stage: "model_verified", ActualChannel: c.ChannelPlayground, RequestID: fixtureRequestID, UpstreamStatus: &status, Versions: in.Account.Versions}}, nil
	}
	accepted, err = s.Submit(context.Background(), admission(t, c.TaskTest, "wrong-target", taskInput{Model: c.DefaultModel, Action: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	r = awaitTerminal(t, store, accepted.TaskID)
	if r.Items[0].Error.Code != c.CodeVerificationFailed {
		t.Fatal("another account proved target", r)
	}
}

func TestWorkerQueueBoundRevocationAndAuditFailure(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return c.VerificationOutput{}, errors.New("synthetic failure")
		case <-ctx.Done():
			return c.VerificationOutput{}, ctx.Err()
		}
	}}
	s := worker(t, store, accounts, verifier, 1)
	first, err := s.Submit(context.Background(), admission(t, c.TaskImport, "first", taskInput{Verify: true, Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	second, err := s.Submit(context.Background(), admission(t, c.TaskImport, "second", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Submit(context.Background(), admission(t, c.TaskImport, "full", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if c.PublicError(err).Code != c.CodeRateLimited {
		t.Fatal(err)
	}
	if _, err = store.RevokeKey(context.Background(), fixtureKeyID); err != nil {
		t.Fatal(err)
	}
	close(release)
	r := awaitTerminal(t, store, second.TaskID)
	if r.Status != c.TaskCancelled {
		t.Fatal("revoked queued task was not cancelled")
	}
	_ = awaitTerminal(t, store, first.TaskID)
	if accounts.imports != 1 {
		t.Fatal("revoked queued work ran")
	}
	_, err = s.Submit(context.Background(), admission(t, c.TaskImport, "revoked", taskInput{Credentials: []c.Credentials{fixtureCredentials()}}))
	if c.PublicError(err).Code != c.CodeUnauthorized {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.active = true
	store.auditErr = true
	store.mu.Unlock()
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskImport, "audit", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	r = awaitTerminal(t, store, accepted.TaskID)
	if r.Items[0].Error.Code != c.CodeAuditPersistenceFailed || r.Items[0].Upload == nil || r.Items[0].Result == nil {
		t.Fatal("audit failure erased commit evidence", r)
	}
}

func TestWorkerLifecycleBatchAndReload(t *testing.T) {
	store := newMemoryStore()
	seedAccount(store, true)
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{}
	s := worker(t, store, accounts, verifier, 100)
	for _, tc := range []struct {
		kind              c.TaskKind
		action            string
		archived, enabled bool
	}{
		{c.TaskArchive, "", true, false}, {c.TaskRestore, "", false, false}, {c.TaskBatch, "enable", false, true}, {c.TaskReload, "", false, true}, {c.TaskReloadAuth, "", false, true},
	} {
		a := admission(t, tc.kind, string(tc.kind)+tc.action, taskInput{Action: tc.action, Model: c.DefaultModel})
		if tc.kind == c.TaskReloadAuth {
			a.Items[0].AccountID = ""
		}
		accepted, err := s.Submit(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		r := awaitTerminal(t, store, accepted.TaskID)
		if r.Status != c.TaskSucceeded {
			t.Fatal(tc.kind, r)
		}
		account, _ := store.GetAccount(context.Background(), fixtureAccountID)
		if account.Archived != tc.archived || account.Enabled != tc.enabled {
			t.Fatal(tc.kind, account)
		}
	}
	if verifier.calls != 0 {
		t.Fatal("lifecycle operation generated a verification")
	}
}

func TestWorkerReceiptVersionsSurviveConcurrentStateReadback(t *testing.T) {
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	verifier := &fakeVerifier{}
	accounts.commitHook = func(context.Context) {
		store.mu.Lock()
		defer store.mu.Unlock()
		for id, live := range store.accounts {
			live.Enabled = false
			live.StateVersion = 8
			live.CredentialVersion = 9
			store.accounts[id] = live
		}
	}
	s := worker(t, store, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskImport, "readback", taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}}))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	item := r.Items[0]
	if r.Status != c.TaskSucceeded || item.Upload == nil || item.Result.Upload.CredentialVersion != 1 || item.Result.Upload.StateVersion != 1 || item.Upload.Versions != item.Result.Upload.Versions {
		t.Fatal("live readback rewrote immutable upload", r)
	}
	live, _ := store.GetAccount(context.Background(), item.AccountID)
	if live.Enabled || live.StateVersion != 8 || live.CredentialVersion != 9 {
		t.Fatal("task restored newer state", live)
	}
}
