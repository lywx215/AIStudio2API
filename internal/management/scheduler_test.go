package management

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type faultStore struct {
	*memoryStore
	claim func(context.Context, string) (c.TaskWork, error)
	get   func(context.Context, string) (c.TaskRecord, error)
	admit func(context.Context, c.Admission) (c.AdmittedTask, error)
	save  func(context.Context, c.TaskRecord, uint64) (c.TaskRecord, error)
	query func(context.Context, c.QueuedTaskQuery) (c.QueuedTaskPage, error)
}

func (f *faultStore) ClaimTask(ctx context.Context, id string) (c.TaskWork, error) {
	if f.claim != nil {
		return f.claim(ctx, id)
	}
	return f.memoryStore.ClaimTask(ctx, id)
}
func (f *faultStore) GetTask(ctx context.Context, id string) (c.TaskRecord, error) {
	if f.get != nil {
		return f.get(ctx, id)
	}
	return f.memoryStore.GetTask(ctx, id)
}
func (f *faultStore) AdmitTask(ctx context.Context, a c.Admission) (c.AdmittedTask, error) {
	if f.admit != nil {
		return f.admit(ctx, a)
	}
	return f.memoryStore.AdmitTask(ctx, a)
}
func (f *faultStore) SaveTask(ctx context.Context, r c.TaskRecord, v uint64) (c.TaskRecord, error) {
	if f.save != nil {
		return f.save(ctx, r, v)
	}
	return f.memoryStore.SaveTask(ctx, r, v)
}
func (f *faultStore) ListQueuedTasks(ctx context.Context, q c.QueuedTaskQuery) (c.QueuedTaskPage, error) {
	if f.query != nil {
		return f.query(ctx, q)
	}
	return f.memoryStore.ListQueuedTasks(ctx, q)
}

func uploadAdmission(t *testing.T, key string) c.Admission {
	return admission(t, c.TaskImport, key, taskInput{Model: c.DefaultModel, Credentials: []c.Credentials{fixtureCredentials()}})
}

func TestSchedulerClaimFailureReadsQueuedAndRetriesWithBackoff(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	var calls atomic.Int32
	first := make(chan time.Time, 1)
	second := make(chan time.Time, 1)
	store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
		if calls.Add(1) == 1 {
			first <- time.Now()
			return c.TaskWork{}, c.NewError(c.CodePersistenceError)
		}
		second <- time.Now()
		return base.ClaimTask(ctx, id)
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), uploadAdmission(t, "retry"))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Status != c.TaskSucceeded || accounts.imports != 1 || calls.Load() != 2 || base.recoveries != 1 {
		t.Fatalf("lost or replayed task: status=%s imports=%d claims=%d recovery=%d", r.Status, accounts.imports, calls.Load(), base.recoveries)
	}
	if elapsed := (<-second).Sub(<-first); elapsed < 150*time.Millisecond {
		t.Fatalf("retry did not back off: %s", elapsed)
	}
}

func TestSchedulerAmbiguousClaimInterruptsWithoutBusiness(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	var claims, failedReads atomic.Int32
	store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
		claims.Add(1)
		_, err := base.ClaimTask(ctx, id)
		if err != nil {
			return c.TaskWork{}, err
		}
		return c.TaskWork{}, c.NewError(c.CodePersistenceError)
	}
	store.get = func(ctx context.Context, id string) (c.TaskRecord, error) {
		if claims.Load() > 0 && failedReads.Add(1) <= 2 {
			return c.TaskRecord{}, c.NewError(c.CodePersistenceError)
		}
		return base.GetTask(ctx, id)
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), uploadAdmission(t, "ambiguous"))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Status != c.TaskInterrupted || accounts.imports != 0 || verifier.calls != 0 || claims.Load() != 1 || base.recoveries != 1 {
		t.Fatalf("ambiguous claim executed: %+v claims=%d", r.Task, claims.Load())
	}
	base.mu.Lock()
	_, hasInput := base.inputs[accepted.TaskID]
	base.mu.Unlock()
	if hasInput {
		t.Fatal("interrupted private input retained")
	}
}

func TestSchedulerUnresolvedClaimDoesNotBlockFollowingQueuedTask(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	var firstID atomic.Value
	firstID.Store("")
	var allowRead atomic.Bool
	var failedClaims atomic.Int32
	var pageAfter atomic.Int32
	store.admit = func(ctx context.Context, a c.Admission) (c.AdmittedTask, error) {
		result, err := base.AdmitTask(ctx, a)
		if err == nil {
			if a.IdempotencyKey == "unresolved" {
				firstID.Store(result.Accepted.TaskID)
			} else {
				base.mu.Lock()
				r := base.tasks[result.Accepted.TaskID]
				r.CreatedAt = c.NewTimestamp(fixtureTime().Add(time.Millisecond))
				base.tasks[result.Accepted.TaskID] = r
				base.mu.Unlock()
			}
		}
		return result, err
	}
	store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
		if id == firstID.Load().(string) && failedClaims.Add(1) == 1 {
			return c.TaskWork{}, c.NewError(c.CodePersistenceError)
		}
		return base.ClaimTask(ctx, id)
	}
	store.get = func(ctx context.Context, id string) (c.TaskRecord, error) {
		if id == firstID.Load().(string) && !allowRead.Load() {
			return c.TaskRecord{}, c.NewError(c.CodePersistenceError)
		}
		return base.GetTask(ctx, id)
	}
	store.query = func(ctx context.Context, q c.QueuedTaskQuery) (c.QueuedTaskPage, error) {
		q.Limit = 1
		if q.After != nil {
			pageAfter.Add(1)
		}
		return base.ListQueuedTasks(ctx, q)
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	bad, err := s.Submit(context.Background(), uploadAdmission(t, "unresolved"))
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.Submit(context.Background(), uploadAdmission(t, "following"))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, good.TaskID)
	if r.Status != c.TaskSucceeded || failedClaims.Load() != 1 {
		t.Fatal("failed readback was treated as another claim", r.Status, failedClaims.Load())
	}
	if pageAfter.Load() == 0 {
		t.Fatal("bounded cursor never advanced")
	}
	allowRead.Store(true)
	s.signal()
	r = awaitTerminal(t, base, bad.TaskID)
	if r.Status != c.TaskSucceeded || accounts.imports != 2 || base.recoveries != 1 {
		t.Fatal("unresolved task was lost or replayed", r.Status, accounts.imports)
	}
}

func TestSchedulerDiscoversCommittedAdmissionAfterLostAcknowledgement(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	acceptedID := make(chan string, 1)
	store.admit = func(ctx context.Context, a c.Admission) (c.AdmittedTask, error) {
		result, err := base.AdmitTask(ctx, a)
		if err != nil {
			return result, err
		}
		acceptedID <- result.Accepted.TaskID
		return c.AdmittedTask{}, c.NewError(c.CodePersistenceError)
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	_, err := s.Submit(context.Background(), uploadAdmission(t, "lost-ack"))
	if c.PublicError(err).Code != c.CodePersistenceError {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, <-acceptedID)
	if r.Status != c.TaskSucceeded || accounts.imports != 1 || base.recoveries != 1 {
		t.Fatal("DB queued fact was lost", r.Status, accounts.imports)
	}
}

func TestSchedulerStartedBusinessIsNeverRetriedAfterStateWriteFailure(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	var failed atomic.Bool
	var claims atomic.Int32
	store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
		claims.Add(1)
		return base.ClaimTask(ctx, id)
	}
	store.save = func(ctx context.Context, r c.TaskRecord, revision uint64) (c.TaskRecord, error) {
		if len(r.Items) > 0 && r.Items[0].Status == c.TaskSucceeded && failed.CompareAndSwap(false, true) {
			return c.TaskRecord{}, c.NewError(c.CodePersistenceError)
		}
		return base.SaveTask(ctx, r, revision)
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), uploadAdmission(t, "write-failed"))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Status != c.TaskInterrupted || r.Items[0].Upload == nil || r.Items[0].Result.Upload == nil || accounts.imports != 1 || claims.Load() != 1 || verifier.calls != 0 {
		t.Fatalf("uncertain completion replayed or lost evidence: %+v imports=%d claims=%d", r.Task, accounts.imports, claims.Load())
	}
}

func TestSchedulerCancelAndRevokeDuringClaimBackoff(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "cancel"
		if revoke {
			name = "revoke"
		}
		t.Run(name, func(t *testing.T) {
			base := newMemoryStore()
			store := &faultStore{memoryStore: base}
			accounts := &fakeAccounts{store: base}
			verifier := &fakeVerifier{}
			attempted := make(chan struct{}, 1)
			store.claim = func(context.Context, string) (c.TaskWork, error) {
				select {
				case attempted <- struct{}{}:
				default:
				}
				return c.TaskWork{}, c.NewError(c.CodePersistenceError)
			}
			s := workerWithStore(t, store, accounts, verifier, 100)
			accepted, err := s.Submit(context.Background(), uploadAdmission(t, name))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-attempted:
			case <-time.After(time.Second):
				t.Fatal("claim not attempted")
			}
			if revoke {
				_, err = base.RevokeKey(context.Background(), fixtureKeyID)
				if err == nil {
					err = s.CancelForKey(context.Background(), fixtureKeyID)
				}
			} else {
				_, err = s.Cancel(context.Background(), accepted.TaskID, c.Principal{Scopes: []c.Scope{c.ScopeTasksWrite}})
			}
			if err != nil {
				t.Fatal(err)
			}
			r := awaitTerminal(t, base, accepted.TaskID)
			if r.Status != c.TaskCancelled || accounts.imports != 0 || verifier.calls != 0 {
				t.Fatal("cancelled queued work executed", r.Task)
			}
		})
	}
}

func TestSchedulerCloseStopsIdleAndBackoffTimers(t *testing.T) {
	for _, backoff := range []bool{false, true} {
		name := "idle"
		if backoff {
			name = "backoff"
		}
		t.Run(name, func(t *testing.T) {
			base := newMemoryStore()
			store := &faultStore{memoryStore: base}
			accounts := &fakeAccounts{store: base}
			verifier := &fakeVerifier{}
			observed := make(chan struct{}, 1)
			var queries atomic.Int32
			store.query = func(ctx context.Context, q c.QueuedTaskQuery) (c.QueuedTaskPage, error) {
				queries.Add(1)
				select {
				case observed <- struct{}{}:
				default:
				}
				return base.ListQueuedTasks(ctx, q)
			}
			attempted := make(chan struct{}, 1)
			if backoff {
				store.claim = func(context.Context, string) (c.TaskWork, error) {
					select {
					case attempted <- struct{}{}:
					default:
					}
					return c.TaskWork{}, c.NewError(c.CodePersistenceError)
				}
			}
			s := workerWithStore(t, store, accounts, verifier, 100)
			if backoff {
				if _, err := s.Submit(context.Background(), uploadAdmission(t, "backoff-close")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-observed:
			case <-time.After(time.Second):
				t.Fatal("queue not scanned")
			}
			if backoff {
				select {
				case <-attempted:
				case <-time.After(time.Second):
					t.Fatal("backoff path not entered")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			after := queries.Load()
			time.Sleep(20 * time.Millisecond)
			if queries.Load() != after {
				t.Fatal("queue timer survived Close")
			}
			select {
			case <-s.done:
			default:
				t.Fatal("worker not joined")
			}
		})
	}
}

func TestClaimBackoffIsBounded(t *testing.T) {
	delay := time.Duration(0)
	for _, want := range []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 5 * time.Second, 5 * time.Second} {
		delay = nextClaimBackoff(delay)
		if delay != want {
			t.Fatalf("got %s want %s", delay, want)
		}
	}
}

func TestSchedulerMalformedPrivateInputDoesNotStarveFollowingQueue(t *testing.T) {
	base := newMemoryStore()
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	bad := uploadAdmission(t, "bad-private")
	bad.PrivateInput = json.RawMessage(`{"credentials":"invalid-shape"}`)
	bad.QueueLimit = 100
	first, err := base.AdmitTask(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	good := uploadAdmission(t, "following-private")
	good.QueueLimit = 100
	second, err := base.AdmitTask(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	_ = worker(t, base, accounts, verifier, 100)
	r := awaitTerminal(t, base, first.Accepted.TaskID)
	other := awaitTerminal(t, base, second.Accepted.TaskID)
	if r.Status != c.TaskInterrupted || other.Status != c.TaskSucceeded || accounts.imports != 1 || base.recoveries != 1 {
		t.Fatal("malformed task blocked another queue row", r.Status, other.Status, accounts.imports)
	}
}

func TestSchedulerMissingBatchActionCannotBecomeDisable(t *testing.T) {
	base := newMemoryStore()
	seedAccount(base, true)
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	s := worker(t, base, accounts, verifier, 100)
	accepted, err := s.Submit(context.Background(), admission(t, c.TaskBatch, "bad-action", taskInput{}))
	if err != nil {
		t.Fatal(err)
	}
	r := awaitTerminal(t, base, accepted.TaskID)
	row, _ := base.GetAccount(context.Background(), fixtureAccountID)
	if r.Status != c.TaskInterrupted || accounts.patches != 0 || !row.Enabled || row.StateVersion != 6 {
		t.Fatal("bad private action executed a disable", r.Task, row.Account)
	}
}

func TestSchedulerHTTPCancelBetweenClaimAndExecutionStaysCancelled(t *testing.T) {
	base := newMemoryStore()
	store := &faultStore{memoryStore: base}
	accounts := &fakeAccounts{store: base}
	verifier := &fakeVerifier{}
	claimed := make(chan string, 1)
	release := make(chan struct{})
	store.claim = func(ctx context.Context, id string) (c.TaskWork, error) {
		work, err := base.ClaimTask(ctx, id)
		if err != nil {
			return work, err
		}
		claimed <- id
		<-release
		return work, nil
	}
	s := workerWithStore(t, store, accounts, verifier, 100)
	h, err := NewHTTP(c.HTTPOptions{Store: store, Accounts: accounts, Tasks: s, Projection: &fakeProjection{}, Clock: fixtureTime})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Submit(context.Background(), uploadAdmission(t, "cancel-after-claim"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-claimed:
		if id != accepted.TaskID {
			t.Fatal("wrong claim target")
		}
	case <-time.After(time.Second):
		t.Fatal("claim did not reach return barrier")
	}
	w := request(t, h.Handler(), "POST", c.Namespace+"/tasks/"+accepted.TaskID+"/cancel", "", nil)
	if w.Code != 200 {
		close(release)
		t.Fatal(w.Code, w.Body.String())
	}
	close(release)
	r := awaitTerminal(t, base, accepted.TaskID)
	if r.Status != c.TaskCancelled || r.Error == nil || r.Error.Code != c.CodeCancelled || r.Items[0].Status != c.TaskCancelled || r.Items[0].Error == nil || r.Items[0].Error.Code != c.CodeCancelled || accounts.imports != 0 || accounts.replaces != 0 || accounts.patches != 0 || verifier.calls != 0 {
		t.Fatal("pre-execution HTTP cancellation became interruption or business", r.Task)
	}
}
