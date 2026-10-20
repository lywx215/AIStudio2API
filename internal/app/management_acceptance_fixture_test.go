package app

// G6 owns this test-only composition. No production config, browser, upstream,
// credential loader, or runtime manager is constructed here.
import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/management"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementstore"
	"github.com/Mag1cFall/AIStudio2API/internal/managementverify"
)

type g6Fixture struct {
	store    c.Store
	accounts c.AccountCoordinator
	tasks    c.TaskService
	verifier c.Verifier
	target   *g6Target
	http     http.Handler
	keys     map[string]c.KeyCreated
	observer *g6Observer
}

func newG6Fixture(t *testing.T, directory string) *g6Fixture {
	t.Helper()
	ctx := context.Background()
	realStore, err := managementstore.Open(ctx, c.StoreOptions{Path: filepath.Join(directory, "management.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	observer := &g6Observer{mutations: map[c.MutationKind]int{}}
	store := &g6ObservedStore{Store: realStore, observer: observer}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	auth := filepath.Join(directory, "synthetic-auth")
	if err := os.MkdirAll(auth, 0700); err != nil {
		t.Fatal(err)
	}
	accounts, err := newManagementAccountCoordinator(c.AccountCoordinatorOptions{
		Store: store, AuthPaths: []string{auth}, JournalDir: filepath.Join(directory, "synthetic-journal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := accounts.BindRuntime(ctx, &g6Runtime{records: map[string]c.AccountRecord{}}); err != nil {
		t.Fatal(err)
	}
	target := &g6Target{}
	verifier, err := managementverify.New(c.VerifierOptions{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := verifier.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	tasks, err := management.NewTaskService(c.TaskServiceOptions{Store: store, Accounts: accounts, Verifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tasks.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	registrar, err := management.NewHTTP(c.HTTPOptions{Store: store, Accounts: accounts, Tasks: tasks, Projection: g6Projection{store}})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]c.KeyCreated{}
	for _, template := range []string{"admin", "operator", "readonly"} {
		key, err := store.CreateKey(ctx, c.KeyCreateRequest{Name: "g6-synthetic-" + template, Scopes: c.ScopeTemplate(template)})
		if err != nil {
			t.Fatal(err)
		}
		keys[template] = key
	}
	// The native router/session middleware are real. Only the public model service
	// is a no-network dependency; accepting a generation there is always an error.
	handler := rootHandler(api.NewHandler(g6PublicService{}, api.Config{
		APIKey: "g6-synthetic-model-key", AdminAuthEnabled: true,
		AdminUsername: "g6-synthetic-console", AdminPassword: "g6-synthetic-password", Management: registrar,
	}))
	return &g6Fixture{store: store, accounts: accounts, tasks: tasks, verifier: verifier, target: target, http: handler, keys: keys, observer: observer}
}

// TestManagementAcceptanceFixtureProcess is an opt-in, one-process loopback
// fixture for the real Python clients. Ordinary go test does not start a server.
func TestManagementAcceptanceFixtureProcess(t *testing.T) {
	if os.Getenv("G6_MANAGEMENT_FIXTURE") != "1" {
		t.Skip("opt-in synthetic loopback fixture")
	}
	directory := os.Getenv("G6_MANAGEMENT_ROOT")
	nonce := os.Getenv("G6_MANAGEMENT_NONCE")
	if !filepath.IsAbs(directory) || len(nonce) != 32 {
		t.Fatal("explicit temporary root and nonce required")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal("temporary root does not exist")
	}
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal("temporary root unavailable")
	}
	rel, err := filepath.Rel(tempRoot, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		t.Fatal("fixture root must be a child of the OS temporary directory")
	}
	if !strings.HasPrefix(filepath.Base(resolved), "aistudio2api-g6-") {
		t.Fatal("unexpected fixture directory name")
	}
	marker, err := os.ReadFile(filepath.Join(resolved, ".g6-fixture"))
	if err != nil || subtle.ConstantTimeCompare(marker, []byte(nonce)) != 1 {
		t.Fatal("temporary root marker mismatch")
	}
	// Refuse symlinks anywhere in the fixture's persistent subtree on restart.
	if err := filepath.WalkDir(resolved, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in fixture root")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f := newG6Fixture(t, resolved)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var once sync.Once
	controlToken := f.keys["admin"].Token
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__g6__/") {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+controlToken)) != 1 {
				http.Error(w, "fixture authorization required", 403)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			switch {
			case r.Method == "GET" && r.URL.Path == "/__g6__/state":
				stats := f.observer.snapshot()
				for name, count := range f.target.snapshot() {
					stats[name] = count
				}
				_ = json.NewEncoder(w).Encode(stats)
			case r.Method == "POST" && r.URL.Path == "/__g6__/barrier":
				var input struct {
					Point string `json:"point"`
				}
				if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&input); err != nil || !f.observer.arm(input.Point) {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, "{}")
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"armed": true})
			case r.Method == "POST" && r.URL.Path == "/__g6__/release":
				f.observer.release()
				_ = json.NewEncoder(w).Encode(map[string]bool{"released": true})
			case r.Method == "POST" && r.URL.Path == "/__g6__/auth-expired":
				var input struct {
					AccountID string `json:"accountId"`
				}
				decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&input); err != nil || !c.ValidAccountID(input.AccountID) {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, "{}")
					return
				}
				account, err := f.authExpired(r.Context(), resolved, input.AccountID)
				if err != nil {
					public := c.PublicError(err)
					w.WriteHeader(public.HTTPStatus())
					_ = json.NewEncoder(w).Encode(map[string]any{"error": public})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"account": account})
			case r.Method == "POST" && r.URL.Path == "/__g6__/native-restore":
				var input struct {
					AccountID string `json:"accountId"`
				}
				decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&input); err != nil || !c.ValidAccountID(input.AccountID) {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, "{}")
					return
				}
				current, err := f.store.GetAccount(r.Context(), input.AccountID)
				if err == nil && (current.AccountName == nil || !strings.HasSuffix(strings.ToLower(current.NativeID), "@example.invalid")) {
					err = c.NewError(c.CodeInvalidState)
				}
				if err != nil {
					public := c.PublicError(err)
					w.WriteHeader(public.HTTPStatus())
					_ = json.NewEncoder(w).Encode(map[string]any{"error": public})
					return
				}
				credential, state := current.CredentialVersion, current.StateVersion
				result, err := f.accounts.Restore(r.Context(), c.StateCommand{AccountID: current.AccountID, Expected: c.ExpectedVersions{Credential: &credential, State: &state}})
				if err != nil {
					public := c.PublicError(err)
					w.WriteHeader(public.HTTPStatus())
					_ = json.NewEncoder(w).Encode(map[string]any{"error": public})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"account": result.Account.Account})
			case r.Method == "POST" && r.URL.Path == "/__g6__/claim-fault":
				var input struct {
					Mode  string `json:"mode"`
					Count int    `json:"count"`
				}
				decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&input); err != nil || !f.observer.armClaimFault(input.Mode, input.Count) {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, "{}")
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"armed": true})
			case r.Method == "POST" && r.URL.Path == "/__g6__/stop":
				_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1024))
				_ = r.Body.Close()
				ack := []byte(`{"stopping":true}`)
				w.Header().Set("Content-Length", strconv.Itoa(len(ack)))
				w.Header().Set("Connection", "close")
				_, _ = w.Write(ack)
				// Flush the test-control acknowledgement before the main test can
				// finish and exit this one-shot process (Windows otherwise races
				// process teardown against the client's HTTP status-line read).
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				once.Do(func() { close(stop) })
			default:
				w.WriteHeader(404)
				_, _ = io.WriteString(w, "{}")
			}
			return
		}
		// Recognizable credentials must be synthetic, including string-encoded
		// storage state. Structurally malformed requests reach the real parser.
		if (r.Method == "POST" && r.URL.Path == c.Namespace+"/accounts/import") ||
			(r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/credentials")) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, c.MaxBodyBytes+1))
			_ = r.Body.Close()
			if err != nil || !g6SyntheticBody(raw) {
				http.Error(w, "only synthetic fixture credentials accepted", 403)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		f.http.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	keyOutput := map[string]map[string]string{}
	for name, key := range f.keys {
		keyOutput[name] = map[string]string{"token": key.Token, "keyId": key.Key.ID}
	}
	ready, err := json.Marshal(map[string]any{"protocol": "g6-fixture-v1", "syntheticOnly": true, "origin": "http://" + listener.Addr().String(), "keys": keyOutput})
	if err != nil {
		t.Fatal(err)
	}
	// Optional consumers may read this atomic, private file instead of stdout.
	// It has the same synthetic-only record and never enters a persistent report.
	readyPath := filepath.Join(resolved, "ready.json")
	if err := os.WriteFile(readyPath+".tmp", ready, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(readyPath+".tmp", readyPath); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("G6_READY %s\n", ready)
	select {
	case <-stop:
	case err := <-done:
		t.Fatalf("fixture server stopped early: %v", err)
	case <-time.After(15 * time.Minute):
		t.Error("fixture lifetime exceeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}

// authExpired is a test-only definitive authentication observation, committed
// through the real coordinator/store/file participant, never a GET projection.
// Only identities already admitted as synthetic into this private fixture may
// be selected. Caller cannot supply flags, versions, filenames or credentials.
func (f *g6Fixture) authExpired(ctx context.Context, directory, accountID string) (c.Account, error) {
	current, err := f.store.GetAccount(ctx, accountID)
	if err != nil {
		return c.Account{}, err
	}
	if current.AccountName == nil || !strings.HasSuffix(strings.ToLower(*current.AccountName), "@example.invalid") || !strings.HasSuffix(strings.ToLower(current.NativeID), "@example.invalid") || current.Archived || !current.Enabled || current.DisabledReason != nil && *current.DisabledReason == "manual" {
		return c.Account{}, c.NewError(c.CodeInvalidState)
	}
	next := current.AccountState
	reason, status, at := "auth_required", 401, c.NewTimestamp(time.Now())
	next.Expired = true
	next.DisabledReason = &reason
	next.DisabledStatus = &status
	next.DisabledAt = &at
	credential, state := current.CredentialVersion, current.StateVersion
	participant := &aistudio.AccountFileParticipant{AuthPaths: []string{filepath.Join(directory, "synthetic-auth")}, JournalDir: filepath.Join(directory, "synthetic-journal")}
	result, err := f.accounts.CommitNative(ctx, c.CommitIntent{Kind: c.MutationState, AccountID: current.AccountID, NativeID: current.NativeID, Expected: c.ExpectedVersions{Credential: &credential, State: &state}, State: &next, CredentialDigest: current.CredentialDigest}, participant)
	return result.Account.Account, err
}

func g6SyntheticBody(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if name, ok := node["accountName"].(string); ok && !strings.HasSuffix(strings.ToLower(name), "@example.invalid") {
				return false
			}
			if cookies, ok := node["cookies"].([]any); ok {
				for _, item := range cookies {
					if cookie, ok := item.(map[string]any); ok {
						if val, ok := cookie["value"].(string); ok && !strings.HasPrefix(val, "g6-synthetic-") {
							return false
						}
					}
				}
			}
			if origins, ok := node["origins"].([]any); ok && len(origins) > 0 {
				return false
			}
			for name, item := range node {
				if name == "credentials" {
					if encoded, ok := item.(string); ok {
						var nested any
						if json.Unmarshal([]byte(encoded), &nested) == nil && !visit(nested) {
							return false
						}
					}
				}
				if !visit(item) {
					return false
				}
			}
		case []any:
			for _, item := range node {
				if !visit(item) {
					return false
				}
			}
		}
		return true
	}
	return visit(value)
}

// The fake sits at the documented upstream boundary, after real HTTP admission,
// real task persistence and the real account credential snapshot/coordinator.
type g6Target struct {
	mu                        sync.Mutex
	opened, generated, closed int
	playground, build         int
}

func (f *g6Target) snapshot() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]int{"opened": f.opened, "generated": f.generated, "closed": f.closed,
		"generationCalls": f.generated, "playgroundCalls": f.playground, "buildCalls": f.build}
}
func (f *g6Target) Open(_ context.Context, input c.VerificationInput) (c.VerificationSession, error) {
	f.mu.Lock()
	f.opened++
	f.mu.Unlock()
	return &g6Session{target: f, input: input}, nil
}

type g6Session struct {
	target *g6Target
	input  c.VerificationInput
	once   sync.Once
}

func (s *g6Session) Close() error {
	s.once.Do(func() { s.target.mu.Lock(); s.target.closed++; s.target.mu.Unlock() })
	return nil
}
func (s *g6Session) Run(ctx context.Context, probe c.VerificationProbe, _ c.ProgressFunc) (c.VerificationObservation, error) {
	if probe.Mode == c.VerificationModel {
		s.target.mu.Lock()
		s.target.generated++
		if probe.Model == "g6-buildonly" {
			s.target.build++
		} else {
			s.target.playground++
		}
		s.target.mu.Unlock()
	}
	requestID, err := c.NewID("req")
	if err != nil {
		return c.VerificationObservation{}, err
	}
	o := c.VerificationObservation{AccountID: s.input.Account.AccountID, NativeID: s.input.Account.NativeID, ActualChannel: c.ChannelPlayground, Model: probe.Model, RequestID: requestID, UpstreamStatus: 200, VisibleText: "OK", Finished: true, IdentityConfirmed: true}
	switch probe.Model {
	case "g6-wrongacct":
		o.AccountID = "00000000-0000-4000-8000-000000000001"
		o.NativeID = "other@example.invalid"
	case "g6-buildonly":
		o.ActualChannel = c.Channel("build")
	case "g6-empty", "g6-thoughtonly", "g6-finishonly":
		o.VisibleText = ""
	case "g6-missingterminal":
		o.Finished = false
	case "g6-wrongmodel":
		o.Model = "g6-other"
	case "g6-unconfirmed":
		o.IdentityConfirmed = false
	case "g6-timeout":
		return o, context.DeadlineExceeded
	case "g6-cancel":
		<-ctx.Done()
		return o, ctx.Err()
	}
	return o, nil
}

// Synthetic runtime publication, with a real pinned mutation lease contract.
// It neither constructs a browser/worker nor participates in model routing.
type g6Runtime struct {
	mu      sync.Mutex
	records map[string]c.AccountRecord
}

func (*g6Runtime) Epoch() uint64 { return 1 }
func (r *g6Runtime) Snapshot(context.Context) ([]c.NativeAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := []c.NativeAccount{}
	for _, a := range r.records {
		if !a.Deleted {
			rows = append(rows, c.NativeAccount{NativeID: a.NativeID, AccountName: a.AccountName, State: a.AccountState, CredentialDigest: a.CredentialDigest})
		}
	}
	return rows, nil
}
func (r *g6Runtime) AcquireMutation(ctx context.Context, _ string, _ bool) (c.AccountMutationLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	return &g6Lease{runtime: r}, nil
}

func (r *g6Runtime) AcquireNativeMutation(ctx context.Context, proof c.NativeLeaseProof) (c.AccountMutationLease, error) {
	if proof == nil || !proof.NativeLeaseActive() || proof.NativeLeaseEpoch() != r.Epoch() {
		return nil, c.NewError(c.CodeVersionConflict)
	}
	return r.AcquireMutation(ctx, proof.NativeLeaseAccountID(), false)
}

// Instrumentation delegates every operation to the real store/participant. Its
// barriers are in test-only local participant calls, outside SQLite transactions.
// No hook, success response, task row or receipt is injected into production.
type g6Observer struct {
	mu                                         sync.Mutex
	admitted                                   int
	mutations                                  map[c.MutationKind]int
	point, reached                             string
	resume                                     chan struct{}
	claimMode                                  string
	claimRemaining, claimAttempts, claimFaults int
}

func (o *g6Observer) snapshot() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	mutations := map[string]int{}
	for kind, count := range o.mutations {
		mutations[string(kind)] = count
	}
	return map[string]any{"admittedTasks": o.admitted, "mutations": mutations, "barrierArmed": o.point, "barrierReached": o.reached,
		"claimAttempts": o.claimAttempts, "claimFaults": o.claimFaults, "claimFaultRemaining": o.claimRemaining}
}
func (o *g6Observer) armClaimFault(mode string, count int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.claimRemaining != 0 || (mode != "before" && mode != "after") || count < 1 || count > 3 {
		return false
	}
	o.claimMode = mode
	o.claimRemaining = count
	return true
}
func (o *g6Observer) arm(point string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.resume != nil {
		return false
	}
	switch point {
	case "before-prepare", "after-prepare", "after-apply", "before-finalize", "after-finalize":
	default:
		return false
	}
	o.point = point
	o.reached = ""
	o.resume = make(chan struct{})
	return true
}
func (o *g6Observer) release() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.resume != nil {
		close(o.resume)
		o.resume = nil
	}
	o.point = ""
	o.reached = ""
}
func (o *g6Observer) wait(ctx context.Context, point string) error {
	o.mu.Lock()
	if o.point != point {
		o.mu.Unlock()
		return nil
	}
	o.reached = point
	resume := o.resume
	o.mu.Unlock()
	select {
	case <-resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type g6ObservedStore struct {
	c.Store
	observer *g6Observer
}

// A "before" fault does not call the underlying ClaimTask: the real row stays
// queued. An "after" fault drops the successful ClaimTask result after the real
// SQLite commit: it is now running, but no private input reaches the worker.
// No task row, HTTP reply, terminal state or model success is fabricated.
func (s *g6ObservedStore) ClaimTask(ctx context.Context, id string) (c.TaskWork, error) {
	s.observer.mu.Lock()
	s.observer.claimAttempts++
	mode := ""
	if s.observer.claimRemaining > 0 {
		mode = s.observer.claimMode
		s.observer.claimRemaining--
		s.observer.claimFaults++
	}
	s.observer.mu.Unlock()
	if mode == "before" {
		return c.TaskWork{}, c.NewError(c.CodePersistenceError)
	}
	result, err := s.Store.ClaimTask(ctx, id)
	if mode == "after" && err == nil {
		return c.TaskWork{}, c.NewError(c.CodePersistenceError)
	}
	return result, err
}

func (s *g6ObservedStore) AdmitTask(ctx context.Context, input c.Admission) (c.AdmittedTask, error) {
	result, err := s.Store.AdmitTask(ctx, input)
	if err == nil && !result.Replayed {
		s.observer.mu.Lock()
		s.observer.admitted++
		s.observer.mu.Unlock()
	}
	return result, err
}
func (s *g6ObservedStore) CommitAccount(ctx context.Context, input c.CommitIntent, participant c.AccountCommitParticipant) (c.AccountCommitResult, error) {
	result, err := s.Store.CommitAccount(ctx, input, &g6BarrierParticipant{AccountCommitParticipant: participant, observer: s.observer})
	if result.Account.AccountID != "" {
		s.observer.mu.Lock()
		s.observer.mutations[input.Kind]++
		s.observer.mu.Unlock()
	}
	return result, err
}

type g6BarrierParticipant struct {
	c.AccountCommitParticipant
	observer *g6Observer
}

func (p *g6BarrierParticipant) Prepare(ctx context.Context, plan c.CommitPlan) (string, error) {
	if err := p.observer.wait(ctx, "before-prepare"); err != nil {
		return "", err
	}
	ref, err := p.AccountCommitParticipant.Prepare(ctx, plan)
	if err != nil {
		return ref, err
	}
	return ref, p.observer.wait(ctx, "after-prepare")
}
func (p *g6BarrierParticipant) Apply(ctx context.Context, record c.RecoveryRecord) error {
	if err := p.AccountCommitParticipant.Apply(ctx, record); err != nil {
		return err
	}
	return p.observer.wait(ctx, "after-apply")
}
func (p *g6BarrierParticipant) Finalize(ctx context.Context, record c.RecoveryRecord) error {
	if err := p.observer.wait(ctx, "before-finalize"); err != nil {
		return err
	}
	if err := p.AccountCommitParticipant.Finalize(ctx, record); err != nil {
		return err
	}
	return p.observer.wait(ctx, "after-finalize")
}

type g6Lease struct {
	runtime *g6Runtime
	once    sync.Once
}

func (*g6Lease) Epoch() uint64 { return 1 }
func (l *g6Lease) Publish(_ context.Context, a c.AccountRecord) error {
	l.runtime.records[a.NativeID] = a
	return nil
}
func (l *g6Lease) Release() error { l.once.Do(func() { l.runtime.mu.Unlock() }); return nil }

type g6Projection struct{ store c.Store }

func (p g6Projection) Status(ctx context.Context) (c.SystemStatus, error) {
	page, err := p.store.ListAccounts(ctx, c.PageQuery{Limit: 200})
	return c.SystemStatus{AccountCount: page.Total, ServiceState: "synthetic_stopped", WAABackend: "go"}, err
}
func (g6Projection) Readiness(context.Context) (c.Readiness, error) {
	return c.Readiness{Ready: false, Checks: []c.Check{{Name: "synthetic_fixture", Ready: false}}}, nil
}
func (g6Projection) Capabilities(context.Context) (c.Capabilities, error) {
	return c.Capabilities{Backend: "aistudio2api", ProtocolVersion: c.ProtocolVersion, StableAccountIDs: true, DualCAS: true, UploadReceipts: true, TargetedVerification: true, VerificationChannels: []c.Channel{c.ChannelPlayground}}, nil
}
func (g6Projection) Settings(context.Context) (c.SettingsSnapshot, error) {
	return c.SettingsSnapshot{Values: c.SettingsValues{WAABackend: "go", WarmStartupConcurrency: 1, PerAccountConcurrency: 1, RoutingStrategy: "round_robin", UpstreamChannels: []string{"playground"}}, PersistentKeys: []string{}}, nil
}
func (g6Projection) PatchSettings(context.Context, c.SettingsPatch) (c.SettingsUpdated, error) {
	return c.SettingsUpdated{}, c.NewError(c.CodeInvalidState)
}
func (g6Projection) Diagnostics(_ context.Context, q c.PageQuery) (c.Diagnostics, error) {
	return c.Diagnostics{ServiceState: "synthetic_stopped", WAABackend: "go", Accounts: c.Page[c.AccountRuntimeState]{Items: []c.AccountRuntimeState{}, Offset: q.Offset, Limit: q.Limit}}, nil
}
func (g6Projection) Usage(_ context.Context, q c.PageQuery) (c.Page[c.UsageRecord], error) {
	return c.Page[c.UsageRecord]{Items: []c.UsageRecord{}, Offset: q.Offset, Limit: q.Limit}, nil
}

type g6PublicService struct{}

func (g6PublicService) Models(context.Context) ([]aistudio.Model, error) {
	return []aistudio.Model{}, nil
}
func (g6PublicService) CountTokens(context.Context, aistudio.TokenCountRequest) (aistudio.TokenCount, error) {
	return aistudio.TokenCount{}, errors.New("synthetic fixture forbids public inference")
}
func (g6PublicService) Generate(context.Context, aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	return nil, errors.New("synthetic fixture forbids public inference")
}
