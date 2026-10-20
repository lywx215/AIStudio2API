package management

import (
	"context"
	"fmt"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Reproduce a queued enable overwriting a newer, explicitly versioned manual
// disable. The second item proves that a stale item does not abort the batch.
func TestQueuedBatchUsesAdmissionVersionsAndContinuesOtherItems(t *testing.T) {
	store := newMemoryStore()
	seedAccount(store, true)
	const secondID = "00000000-0000-4000-8000-000000000010"
	second := store.accounts[fixtureAccountID]
	second.AccountID, second.Index, second.Enabled = secondID, 4, false
	store.accounts[secondID] = second
	accounts := &fakeAccounts{store: store}
	entered := make(chan struct{}, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, store, accounts, verifier, 100)
	blocking, err := s.Submit(context.Background(), admission(t, c.TaskTest, "blocking", taskInput{Model: c.DefaultModel, Action: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocking verification did not start")
	}
	queued := admission(t, c.TaskBatch, "queued-enable", taskInput{Action: "enable", Model: c.DefaultModel})
	queued.Items = append(queued.Items, c.TaskItem{AccountID: secondID, Status: c.TaskQueued, Stage: "queued"})
	accepted, err := s.Submit(context.Background(), queued)
	if err != nil {
		t.Fatal(err)
	}
	cv, sv := uint64(5), uint64(6)
	_, err = accounts.Patch(context.Background(), c.StateCommand{AccountID: fixtureAccountID, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the same request after the disable must keep the original
	// admission snapshot rather than silently refreshing its expected versions.
	replayed, err := s.Submit(context.Background(), queued)
	if err != nil || replayed != accepted {
		t.Fatalf("changed replay acceptance: %+v %v", replayed, err)
	}
	_, err = s.Cancel(context.Background(), blocking.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, store, accepted.TaskID)
	first, _ := store.GetAccount(context.Background(), fixtureAccountID)
	other, _ := store.GetAccount(context.Background(), secondID)
	if first.Enabled || first.StateVersion != 7 || first.CredentialVersion != 5 {
		t.Fatalf("queued enable overwrote newer disable: %+v", first.Account)
	}
	if r.Status != c.TaskPartial || r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeVersionConflict || r.Items[1].Status != c.TaskSucceeded || !other.Enabled {
		t.Fatalf("batch did not preserve conflict and continue: %+v", r.Task)
	}
	if verifier.calls != 1 {
		t.Fatalf("stale item called verifier: %d", verifier.calls)
	}
}

func TestQueuedTargetOperationsRejectChangedAdmissionVersion(t *testing.T) {
	for _, kind := range []c.TaskKind{c.TaskReplace, c.TaskTest, c.TaskReload, c.TaskArchive, c.TaskRestore} {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryStore()
			seedAccount(store, true)
			accounts := &fakeAccounts{store: store}
			entered := make(chan struct{}, 1)
			verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return c.VerificationOutput{}, ctx.Err()
			}}
			s := worker(t, store, accounts, verifier, 100)
			blocking, err := s.Submit(context.Background(), admission(t, c.TaskTest, "blocking", taskInput{Model: c.DefaultModel, Action: "model"}))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("blocking verification did not start")
			}
			in := taskInput{Model: c.DefaultModel, Action: "model", Legacy: true, Verify: true, Force: true, Credentials: []c.Credentials{fixtureCredentials()}}
			accepted, err := s.Submit(context.Background(), admission(t, kind, "queued-target", in))
			if err != nil {
				t.Fatal(err)
			}
			cv, sv := uint64(5), uint64(6)
			_, err = accounts.Patch(context.Background(), c.StateCommand{AccountID: fixtureAccountID, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Cancel(context.Background(), blocking.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
			if err != nil {
				t.Fatal(err)
			}
			r := awaitTerminal(t, store, accepted.TaskID)
			if r.Status != c.TaskFailed || r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeVersionConflict {
				t.Fatalf("stale %s executed: %+v", kind, r.Task)
			}
			if verifier.calls != 1 || accounts.replaces != 0 {
				t.Fatalf("stale work reached collaborators: verify=%d replace=%d", verifier.calls, accounts.replaces)
			}
		})
	}
}

func TestQueuedFutureExpectedDoesNotReplaceAdmissionSnapshot(t *testing.T) {
	base := newMemoryStore()
	seedAccount(base, true)
	accounts := &fakeAccounts{store: base}
	entered := make(chan struct{}, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, base, accounts, verifier, 100)
	block, err := s.Submit(context.Background(), admission(t, c.TaskTest, "block-future", taskInput{Model: c.DefaultModel, Action: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker not blocked")
	}
	cv, sv := uint64(6), uint64(6)
	a := admission(t, c.TaskReplace, "future", taskInput{Model: c.DefaultModel, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Credentials: []c.Credentials{fixtureCredentials()}})
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatalf("explicit mismatch must remain asynchronous: %v", err)
	}
	base.mu.Lock()
	current := base.accounts[fixtureAccountID]
	current.CredentialVersion = 6
	base.accounts[fixtureAccountID] = current
	base.mu.Unlock()
	_, err = s.Cancel(context.Background(), block.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeVersionConflict || accounts.replaces != 0 || verifier.calls != 1 {
		t.Fatal("future expected bypassed frozen observed snapshot", r.Task)
	}
}

func TestReloadAuthUsesFixedPagedAdmissionSet(t *testing.T) {
	base := newMemoryStore()
	seedAccount(base, true)
	for i := 0; i < 205; i++ {
		record := base.accounts[fixtureAccountID]
		record.AccountID = fmt.Sprintf("10000000-0000-4000-8000-%012x", i+1)
		record.Index = int64(i + 10)
		base.accounts[record.AccountID] = record
	}
	accounts := &fakeAccounts{store: base}
	entered := make(chan struct{}, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, base, accounts, verifier, 100)
	block, err := s.Submit(context.Background(), admission(t, c.TaskTest, "block-reload", taskInput{Model: c.DefaultModel, Action: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker not blocked")
	}
	a := admission(t, c.TaskReloadAuth, "fixed-reload", taskInput{})
	a.Items[0].AccountID = ""
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	newRecord := base.accounts[fixtureAccountID]
	newRecord.AccountID = "20000000-0000-4000-8000-000000000001"
	newRecord.Index = 999
	base.accounts[newRecord.AccountID] = newRecord
	changed := base.accounts[fixtureAccountID]
	changed.Enabled = false
	changed.StateVersion++
	base.accounts[fixtureAccountID] = changed
	base.mu.Unlock()
	_, err = s.Cancel(context.Background(), block.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Status != c.TaskFailed || r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeVersionConflict {
		t.Fatal("changed reload target was rebased", r.Task)
	}
	accounts.mu.Lock()
	ids := append([]string{}, accounts.reloadIDs...)
	accounts.mu.Unlock()
	if len(ids) != 205 {
		t.Fatalf("fixed target pages truncated or expanded: %d", len(ids))
	}
	for _, id := range ids {
		if id == fixtureAccountID || id == newRecord.AccountID {
			t.Fatalf("unexpected reloaded target %s", id)
		}
	}
	if verifier.calls != 1 {
		t.Fatal("reload called model", verifier.calls)
	}
}

func TestReloadAuthEmptyManifestAndMissingManifestAreDistinct(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "empty"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			base := newMemoryStore()
			store := &faultStore{memoryStore: base}
			accounts := &fakeAccounts{store: base}
			verifier := &fakeVerifier{}
			if missing {
				store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
					work, err := base.ClaimTask(ctx, id)
					work.PreconditionSet = nil
					return work, err
				}
			}
			s := workerWithStore(t, store, accounts, verifier, 100)
			a := admission(t, c.TaskReloadAuth, name, taskInput{})
			a.Items[0].AccountID = ""
			accepted, err := s.Submit(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			r := awaitTerminal(t, base, accepted.TaskID)
			want := c.TaskSucceeded
			if missing {
				want = c.TaskInterrupted
			}
			if r.Status != want || len(accounts.reloadIDs) != 0 || verifier.calls != 0 {
				t.Fatal("empty/unprotected reload confused", r.Task)
			}
		})
	}
}

func TestMissingAdmissionTargetIsNeverRebound(t *testing.T) {
	base := newMemoryStore()
	seedAccount(base, true)
	accounts := &fakeAccounts{store: base}
	entered := make(chan struct{}, 1)
	verifier := &fakeVerifier{verify: func(ctx context.Context, _ c.VerificationInput) (c.VerificationOutput, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return c.VerificationOutput{}, ctx.Err()
	}}
	s := worker(t, base, accounts, verifier, 100)
	block, err := s.Submit(context.Background(), admission(t, c.TaskTest, "block-missing", taskInput{Model: c.DefaultModel, Action: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker not blocked")
	}
	const missingID = "30000000-0000-4000-8000-000000000001"
	a := admission(t, c.TaskTest, "missing-target", taskInput{Model: c.DefaultModel, Action: "model"})
	a.Items[0].AccountID = missingID
	accepted, err := s.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	row := base.accounts[fixtureAccountID]
	row.AccountID = missingID
	row.Index = 12
	base.accounts[missingID] = row
	base.mu.Unlock()
	_, err = s.Cancel(context.Background(), block.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeNotFound || verifier.calls != 1 {
		t.Fatal("missing snapshot rebound", r.Task)
	}
}
