package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func runtimeAccount(id string) vault.Account {
	value := account()
	value.ID = "acct-" + strings.Repeat(id, 32)
	value.Label = "Synthetic " + id
	return value
}

func runtimeTransport(authCalls *atomic.Int32, assist func(*http.Request) (*http.Response, error)) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getoxsrf") {
			if authCalls != nil {
				authCalls.Add(1)
			}
			return reply(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/widgetCreateSession") {
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		}
		return assist(r)
	}
}

func leaseEngine(t *testing.T, accounts []vault.Account, limit int) *Engine {
	t.Helper()
	e := New(context.Background(), accounts, limit, time.Minute, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("direct lease test must not use a network")
	}))
	t.Cleanup(e.Close)
	return e
}

func mustLease(t *testing.T, e *Engine, id string) *lease {
	t.Helper()
	value, err := e.acquire(context.Background(), []string{id})
	if err != nil {
		t.Fatal("cannot acquire synthetic account", err)
	}
	t.Cleanup(func() { e.release(value, nil) })
	return value
}

func requireFailure(t *testing.T, err error, code int) {
	t.Helper()
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("expected failure %d, got %v", code, err)
	}
}

func runtimeState(t *testing.T, e *Engine, id string) AccountState {
	t.Helper()
	for _, item := range e.Snapshot().Accounts {
		if item.ID == id {
			return item
		}
	}
	t.Fatal("account state missing")
	return AccountState{}
}

func TestReconcileMetadataKeepsStreamTokenAndImmutableTimeout(t *testing.T) {
	value := runtimeAccount("a")
	var authCalls, generations atomic.Int32
	firstPart := make(chan struct{})
	finish := make(chan struct{})
	deadlines := make(chan time.Duration, 2)
	e := New(context.Background(), []vault.Account{value}, 1, time.Minute, runtimeTransport(&authCalls, func(r *http.Request) (*http.Response, error) {
		deadline, _ := r.Context().Deadline()
		deadlines <- time.Until(deadline)
		if generations.Add(1) != 1 {
			return reply(200, stream), nil
		}
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			_, _ = writer.Write([]byte(`[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"role":"model","text":"opening"}}}]}}},`))
			select {
			case <-finish:
				_, _ = writer.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}]`))
			case <-r.Context().Done():
				_ = writer.CloseWithError(r.Context().Err())
			}
		}()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
	}))
	defer e.Close()
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		done <- e.Generate(context.Background(), contract.Selection{ModelID: "observed"}, request(), func(event contract.Event) error {
			if len(event.Parts) > 0 {
				once.Do(func() { close(firstPart) })
			}
			return nil
		})
	}()
	select {
	case <-firstPart:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	before := runtimeState(t, e, value.ID)
	if !before.AuthVerified || before.AuthVerifiedAt.IsZero() || before.State != "busy" || len(before.VerifiedModels) != 0 {
		t.Fatal(before)
	}
	value.Label = "changed metadata"
	e.Reconcile([]vault.Account{value}, 1, 2*time.Second)
	e.Configure(2, 3*time.Second)
	if current := runtimeState(t, e, value.ID); current.AuthVerifiedAt != before.AuthVerifiedAt || !current.AuthVerified || current.State != "busy" {
		t.Fatal(current)
	}
	select {
	case err := <-done:
		t.Fatal("metadata/config cancelled live stream", err)
	default:
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	verified := runtimeState(t, e, value.ID).VerifiedModels["observed"]
	if verified.IsZero() {
		t.Fatal("successful terminal was not recorded")
	}
	e.Reconcile([]vault.Account{value}, 2, 3*time.Second)
	if err := e.Generate(context.Background(), contract.Selection{Auto: true}, request(), func(contract.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state := runtimeState(t, e, value.ID)
	if authCalls.Load() != 1 || state.VerifiedModels["observed"] != verified || state.VerifiedModels["auto"].IsZero() {
		t.Fatal("metadata lost JWT/model verification", state, authCalls.Load())
	}
	oldDeadline, newDeadline := <-deadlines, <-deadlines
	if oldDeadline < 45*time.Second || newDeadline > 3*time.Second || newDeadline < 2*time.Second {
		t.Fatal("lease timeout changed after acquisition", oldDeadline, newDeadline)
	}
}

func TestReconcilePreservesCooldownAuthRequiredAndResetsExplicitCredentialEpoch(t *testing.T) {
	for _, status := range []int{401, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			value := runtimeAccount("a")
			var authCalls atomic.Int32
			var failed atomic.Bool
			e := New(context.Background(), []vault.Account{value}, 1, time.Minute, runtimeTransport(&authCalls, func(*http.Request) (*http.Response, error) {
				if failed.Load() {
					return reply(status, `{}`), nil
				}
				return reply(200, stream), nil
			}))
			defer e.Close()
			generate := func() error {
				return e.Generate(context.Background(), contract.Selection{ModelID: "observed"}, request(), func(contract.Event) error { return nil })
			}
			if err := generate(); err != nil {
				t.Fatal(err)
			}
			verified := runtimeState(t, e, value.ID).VerifiedModels["observed"]
			failed.Store(true)
			if err := generate(); err == nil {
				t.Fatal("upstream failure accepted")
			}
			before := runtimeState(t, e, value.ID)
			want := "cooldown"
			if status == 401 {
				want = "auth_required"
			}
			if before.State != want {
				t.Fatal(before)
			}
			value.Label = "metadata only"
			e.Reconcile([]vault.Account{value}, 2, time.Minute)
			e.Configure(3, 2*time.Minute)
			after := runtimeState(t, e, value.ID)
			if after.State != want || after.VerifiedModels["observed"] != verified {
				t.Fatal("metadata lost runtime state", after)
			}
			if err := generate(); err == nil {
				t.Fatal("metadata bypassed cooldown/auth_required")
			}
			if status == 401 && after.AuthVerified {
				t.Fatal("401 retained an effective JWT")
			}
			value.Epoch++ // same credential bytes, explicitly refreshed by the user
			e.Reconcile([]vault.Account{value}, 1, time.Minute)
			reset := runtimeState(t, e, value.ID)
			if reset.State != "ready" || reset.AuthVerified || !reset.AuthVerifiedAt.IsZero() || len(reset.VerifiedModels) != 0 {
				t.Fatal("credential epoch retained old evidence", reset)
			}
			failed.Store(false)
			if err := generate(); err != nil {
				t.Fatal(err)
			}
			if authCalls.Load() != 2 {
				t.Fatal("new credential epoch did not reauthenticate", authCalls.Load())
			}
		})
	}
}

func TestReconcileCancelsOnlyChangedVersionWaitsAndIgnoresLateRelease(t *testing.T) {
	a, b := runtimeAccount("a"), runtimeAccount("b")
	e := leaseEngine(t, []vault.Account{a, b}, 3)
	oldA, oldB := mustLease(t, e, a.ID), mustLease(t, e, b.ID)
	if err := e.commitToken(oldA, "old-jwt", time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.verifyModel(oldA, contract.Selection{ModelID: "old-model"})
	a.Epoch++
	a.Credential.SecureCSES = "new-synthetic-cookie"
	e.Reconcile([]vault.Account{a, b}, 3, 2*time.Minute)
	if oldA.ctx.Err() == nil || oldB.ctx.Err() != nil || oldA.account.Credential.SecureCSES != "synthetic-cookie" || oldA.timeout != time.Minute {
		t.Fatal("reconcile changed unrelated lease or mutated credential snapshot")
	}
	if state := runtimeState(t, e, a.ID); state.State != "draining" || state.AuthVerified || len(state.VerifiedModels) != 0 {
		t.Fatal(state)
	}
	_, err := e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	a.Epoch++
	a.Credential.SecureCSES = "newest-synthetic-cookie"
	e.Reconcile([]vault.Account{a, b}, 3, 2*time.Minute)
	e.release(oldA, &upstreamError{401})
	newA := mustLease(t, e, a.ID)
	if newA.account.Epoch != a.Epoch || newA.account.Credential.SecureCSES != a.Credential.SecureCSES || newA.timeout != 2*time.Minute {
		t.Fatal("pending version was not the latest")
	}
	if err := e.commitToken(newA, "new-jwt", time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.verifyModel(newA, contract.Selection{ModelID: "new-model"})
	e.release(oldA, &upstreamError{429}) // deliberately late duplicate callback
	if state := runtimeState(t, e, a.ID); state.State != "busy" || !state.AuthVerified || state.VerifiedModels["new-model"].IsZero() || len(state.VerifiedModels) != 1 || e.Snapshot().Active != 2 {
		t.Fatal("old release polluted current version", state, e.Snapshot())
	}
	if err := e.commitToken(oldA, "late-old-jwt", time.Now().Add(time.Minute), time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal("old token committed", err)
	}
}

func TestReconcileDuringAuthenticationCannotCommitAnOldVersionJWT(t *testing.T) {
	a := runtimeAccount("a")
	entered := make(chan struct{})
	allowLateResponse := make(chan struct{})
	var authCalls, generations atomic.Int32
	cookies := make(chan string, 2)
	e := New(context.Background(), []vault.Account{a}, 2, time.Minute, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getoxsrf") {
			cookies <- r.Header.Get("Cookie")
			if authCalls.Add(1) == 1 {
				close(entered)
				// Deliberately emulate an uncooperative/late transport response.
				<-allowLateResponse
			}
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/widgetCreateSession") {
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		}
		generations.Add(1)
		return reply(200, stream), nil
	}))
	defer e.Close()
	done := make(chan error, 1)
	go func() {
		done <- e.Generate(context.Background(), contract.Selection{ModelID: "observed"}, request(), func(contract.Event) error { return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("auth did not start")
	}
	a.Epoch++
	a.Credential.SecureCSES = "new-synthetic-cookie"
	e.Reconcile([]vault.Account{a}, 2, time.Minute)
	if runtimeState(t, e, a.ID).AuthVerified {
		t.Fatal("uncommitted old authentication marked new credential verified")
	}
	_, err := e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	close(allowLateResponse)
	select {
	case err := <-done:
		requireFailure(t, err, 499)
	case <-time.After(time.Second):
		t.Fatal("late auth did not drain")
	}
	if state := runtimeState(t, e, a.ID); state.AuthVerified || len(state.VerifiedModels) != 0 || state.State != "ready" {
		t.Fatal("old auth result leaked into new credential", state)
	}
	if err := e.Generate(context.Background(), contract.Selection{ModelID: "observed"}, request(), func(contract.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if authCalls.Load() != 2 || generations.Load() != 1 || !strings.Contains(<-cookies, "__Secure-C_SES=synthetic-cookie") || !strings.Contains(<-cookies, "__Secure-C_SES=new-synthetic-cookie") {
		t.Fatal("credential snapshots overlapped or generation was replayed")
	}
}

func TestReconcileOffOnAndDisableRetainDrainingIDOwnership(t *testing.T) {
	a, b := runtimeAccount("a"), runtimeAccount("b")
	e := leaseEngine(t, []vault.Account{a, b}, 4)
	oldA, oldB := mustLease(t, e, a.ID), mustLease(t, e, b.ID)
	e.Reconcile(nil, 4, time.Minute)
	if snapshot := e.Snapshot(); snapshot.Closed || snapshot.Active != 2 || len(snapshot.Accounts) != 0 || oldA.ctx.Err() == nil || oldB.ctx.Err() == nil {
		t.Fatal(snapshot)
	}
	e.Reconcile([]vault.Account{a, b}, 4, time.Minute)
	_, err := e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	if runtimeState(t, e, a.ID).State != "draining" {
		t.Fatal("rapid off/on leased an old ID")
	}
	e.release(oldA, nil)
	newA := mustLease(t, e, a.ID)
	disabled := a
	disabled.Enabled = false
	e.Reconcile([]vault.Account{disabled, b}, 4, time.Minute)
	if newA.ctx.Err() == nil || runtimeState(t, e, a.ID).State != "disabled" {
		t.Fatal("disable did not cancel only its lease")
	}
	e.release(newA, nil)
	_, err = e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	e.release(oldB, nil)
	e.Reconcile([]vault.Account{a, b}, 4, time.Minute)
	readyA := mustLease(t, e, a.ID)
	e.Reconcile([]vault.Account{b}, 4, time.Minute)
	if readyA.ctx.Err() == nil || len(e.Snapshot().Accounts) != 1 || e.Snapshot().Accounts[0].ID != b.ID {
		t.Fatal("delete did not isolate its account")
	}
	e.release(readyA, &upstreamError{401})
	e.Reconcile([]vault.Account{a, b}, 4, time.Minute)
	_ = mustLease(t, e, a.ID)
}

func TestSuspendPreservesIdleJWTAuthRequiredCooldownAndVerification(t *testing.T) {
	a, b, c := runtimeAccount("a"), runtimeAccount("b"), runtimeAccount("c")
	e := leaseEngine(t, []vault.Account{a, b, c}, 3)
	aLease, bLease, cLease := mustLease(t, e, a.ID), mustLease(t, e, b.ID), mustLease(t, e, c.ID)
	if err := e.commitToken(aLease, "valid-synthetic-jwt", time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.verifyModel(aLease, contract.Selection{ModelID: "observed"})
	e.release(aLease, nil)
	e.release(bLease, &upstreamError{401})
	e.release(cLease, &upstreamError{429})
	before := runtimeState(t, e, a.ID)
	e.Suspend()
	e.Configure(1, 2*time.Minute)
	if snapshot := e.Snapshot(); !snapshot.Suspended || snapshot.Closed || snapshot.Active != 0 || snapshot.Accounts[0].State != "suspended" {
		t.Fatal(snapshot)
	}
	_, err := e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	e.Reconcile([]vault.Account{a, b, c}, 3, time.Minute)
	after := runtimeState(t, e, a.ID)
	if e.Snapshot().Suspended || !after.AuthVerified || after.AuthVerifiedAt != before.AuthVerifiedAt || after.VerifiedModels["observed"] != before.VerifiedModels["observed"] {
		t.Fatal("off/on lost unchanged idle state", after)
	}
	if runtimeState(t, e, b.ID).State != "auth_required" || runtimeState(t, e, c.ID).State != "cooldown" {
		t.Fatal("off/on bypassed prior auth/cooldown state", e.Snapshot())
	}
}

func TestSuspendResumeWaitsForOldLeaseAndIgnoresItsLateFailure(t *testing.T) {
	a := runtimeAccount("a")
	e := leaseEngine(t, []vault.Account{a}, 2)
	old := mustLease(t, e, a.ID)
	if err := e.commitToken(old, "valid-synthetic-jwt", time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.verifyModel(old, contract.Selection{Auto: true})
	e.Suspend()
	e.Reconcile([]vault.Account{a}, 2, time.Minute)
	if old.ctx.Err() == nil || runtimeState(t, e, a.ID).State != "draining" {
		t.Fatal("resume did not retain ID ownership")
	}
	_, err := e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 503)
	e.release(old, &upstreamError{401})
	current := mustLease(t, e, a.ID)
	state := runtimeState(t, e, a.ID)
	if current.token != "valid-synthetic-jwt" || !state.AuthVerified || state.VerifiedModels["auto"].IsZero() {
		t.Fatal("cancelled old request polluted resumed state", state)
	}
	e.release(old, &upstreamError{429})
	if e.Snapshot().Active != 1 || runtimeState(t, e, a.ID).State != "busy" {
		t.Fatal("late release affected resumed lease")
	}
}

func TestConfigureDynamicallyRaisesAndLowersLimitWithoutCancellingLeases(t *testing.T) {
	a, b, c := runtimeAccount("a"), runtimeAccount("b"), runtimeAccount("c")
	e := leaseEngine(t, []vault.Account{a, b, c}, 1)
	first := mustLease(t, e, a.ID)
	_, err := e.acquire(context.Background(), []string{b.ID})
	requireFailure(t, err, 429)
	e.Configure(3, 2*time.Minute)
	second, third := mustLease(t, e, b.ID), mustLease(t, e, c.ID)
	if second.timeout != 2*time.Minute || third.timeout != 2*time.Minute || first.timeout != time.Minute {
		t.Fatal("leases did not capture independent timeout")
	}
	e.Configure(1, 3*time.Minute)
	for _, item := range []*lease{first, second, third} {
		if item.ctx.Err() != nil {
			t.Fatal("lowered limit cancelled admitted work")
		}
	}
	e.release(first, nil)
	_, err = e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 429)
	e.release(second, nil)
	_, err = e.acquire(context.Background(), []string{a.ID})
	requireFailure(t, err, 429)
	e.release(third, nil)
	last := mustLease(t, e, a.ID)
	if last.timeout != 3*time.Minute || e.Snapshot().Active != 1 || e.Snapshot().Limit != 1 {
		t.Fatal("new settings were not applied to the next lease")
	}
}

func TestRuntimeSnapshotExpiryDeepCopyAndClosedState(t *testing.T) {
	a := runtimeAccount("a")
	e := leaseEngine(t, []vault.Account{a}, 1)
	item := mustLease(t, e, a.ID)
	if err := e.commitToken(item, "synthetic-jwt", time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.verifyModel(item, contract.Selection{Auto: true})
	snapshot := e.Snapshot()
	snapshot.Accounts[0].VerifiedModels["poison"] = time.Now()
	snapshot.Accounts[0].ID = "poison"
	if state := runtimeState(t, e, a.ID); !state.AuthVerified || len(state.VerifiedModels) != 1 {
		t.Fatal("snapshot shared internal state", state)
	}
	e.mu.Lock()
	item.owner.expires = time.Now().Add(-time.Second)
	e.mu.Unlock()
	if runtimeState(t, e, a.ID).AuthVerified {
		t.Fatal("expired JWT remained verified")
	}
	e.Close()
	if snapshot := e.Snapshot(); !snapshot.Closed || snapshot.Accounts[0].AuthVerified || item.ctx.Err() == nil {
		t.Fatal(snapshot)
	}
	e.Reconcile([]vault.Account{a}, 16, time.Hour)
	e.Configure(16, time.Hour)
	_, err := e.acquire(context.Background(), nil)
	requireFailure(t, err, 503)
	e.release(item, nil)
	if e.Snapshot().Active != 0 {
		t.Fatal("closed lease did not drain")
	}
}

func TestModelVerificationRequiresSuccessfulTerminalAndCurrentVersion(t *testing.T) {
	for _, body := range []string{`[]`, `[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS"}}}]`, stream + `{}`, `[{"error":{"code":500}}]`} {
		a := runtimeAccount("a")
		e := New(context.Background(), []vault.Account{a}, 1, time.Minute, runtimeTransport(nil, func(*http.Request) (*http.Response, error) { return reply(200, body), nil }))
		err := e.Generate(context.Background(), contract.Selection{ModelID: "not-verified"}, request(), func(contract.Event) error { return nil })
		if err == nil || len(runtimeState(t, e, a.ID).VerifiedModels) != 0 {
			t.Fatal("incomplete generation marked model verified", body, err)
		}
		e.Close()
	}
	a := runtimeAccount("a")
	e := New(context.Background(), []vault.Account{a}, 1, time.Minute, runtimeTransport(nil, func(*http.Request) (*http.Response, error) { return reply(200, stream), nil }))
	defer e.Close()
	err := e.Generate(context.Background(), contract.Selection{ModelID: "not-verified"}, request(), func(event contract.Event) error {
		if event.Finished {
			return errors.New("synthetic sink failure")
		}
		return nil
	})
	if err == nil || len(runtimeState(t, e, a.ID).VerifiedModels) != 0 {
		t.Fatal("sink failure marked model verified")
	}
	item := mustLease(t, e, a.ID)
	a.Epoch++
	e.Reconcile([]vault.Account{a}, 1, time.Minute)
	e.verifyModel(item, contract.Selection{ModelID: "late-old-model"})
	e.release(item, nil)
	if len(runtimeState(t, e, a.ID).VerifiedModels) != 0 {
		t.Fatal("retired completion leaked to new credential")
	}
}

func TestConcurrentReconcileConfigureSnapshotAndGeneration(t *testing.T) {
	a, b := runtimeAccount("a"), runtimeAccount("b")
	e := New(context.Background(), []vault.Account{a, b}, 2, time.Second, runtimeTransport(nil, func(*http.Request) (*http.Response, error) { return reply(200, stream), nil }))
	defer e.Close()
	var workers sync.WaitGroup
	workers.Add(4)
	go func() {
		defer workers.Done()
		for range 120 {
			e.Configure(1, time.Second)
			e.Configure(2, 2*time.Second)
		}
	}()
	go func() {
		defer workers.Done()
		for i := range 120 {
			next := a
			next.Label = string(rune('a' + i%20))
			if i%9 == 0 {
				next.Epoch++
			}
			e.Reconcile([]vault.Account{next, b}, 2, time.Second)
		}
	}()
	go func() {
		defer workers.Done()
		for range 240 {
			snapshot := e.Snapshot()
			for _, state := range snapshot.Accounts {
				state.VerifiedModels["caller-owned"] = time.Now()
			}
		}
	}()
	go func() {
		defer workers.Done()
		for range 120 {
			_ = e.Generate(context.Background(), contract.Selection{ModelID: "observed"}, request(), func(contract.Event) error { return nil })
		}
	}()
	workers.Wait()
	snapshot := e.Snapshot()
	if snapshot.Active != 0 || snapshot.Closed {
		t.Fatal(snapshot)
	}
	for _, state := range snapshot.Accounts {
		if _, ok := state.VerifiedModels["caller-owned"]; ok {
			t.Fatal("snapshot mutated internal model history")
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(encoded), "synthetic-cookie") || strings.Contains(string(encoded), "synthetic-config") {
		t.Fatal("snapshot exposed credentials", err)
	}
}

// Deterministically cancels between Generate's initial context check and the
// acquisition check, reproducing a real caller cancellation during preflight.
type cancelDuringPreflight struct {
	context.Context
	checks atomic.Int32
	done   chan struct{}
	once   sync.Once
}

func (c *cancelDuringPreflight) Done() <-chan struct{} { return c.done }
func (c *cancelDuringPreflight) Err() error {
	if c.checks.Add(1) < 2 {
		return nil
	}
	c.once.Do(func() { close(c.done) })
	return context.Canceled
}

func TestAcquireCancellationRemainsPublicCancelledFailure(t *testing.T) {
	e := leaseEngine(t, []vault.Account{runtimeAccount("a")}, 1)
	ctx := &cancelDuringPreflight{Context: context.Background(), done: make(chan struct{})}
	err := e.Generate(ctx, contract.Selection{ModelID: "observed"}, request(), func(contract.Event) error { return nil })
	requireFailure(t, err, 499)
	if e.Snapshot().Active != 0 {
		t.Fatal("cancelled preflight leased an account")
	}
}
