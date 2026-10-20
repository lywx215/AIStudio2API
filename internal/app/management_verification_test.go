package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementverify"
)

const verificationAccountUUID = "f596663f-cb0c-41ca-a00d-fc035e7ec075"
const verificationReqUUID = "req_676ad95f-04ae-4ceb-b6ad-614f647772bd"
const verificationWaitTimeout = 5 * time.Second

func verificationPathWithin(root, target string) error {
	absoluteRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("normalize temporary root: %w", err)
	}
	absoluteTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return fmt.Errorf("normalize temporary storage: %w", err)
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteTarget)
	if err != nil {
		return fmt.Errorf("resolve temporary storage: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !filepath.IsLocal(relative) {
		return errors.New("storage outside temporary directory")
	}
	return nil
}

// Error is safe in worker goroutines; Fatal/FailNow would Goexit before Verify
// can return its result and leave the test waiting on an unreachable signal.
func verificationCallbackError(t *testing.T, err error) error {
	t.Helper()
	t.Error(err)
	return err
}

func verificationStorage(t *testing.T, value string) c.CredentialMaterial {
	t.Helper()
	state := aistudio.StorageState{Cookies: []aistudio.StateCookie{}, Origins: []aistudio.StorageOrigin{{Origin: "https://aistudio.google.com", LocalStorage: []aistudio.StorageItem{{Name: "synthetic-local", Value: "preserved"}}}}}
	for _, name := range []string{"SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID"} {
		state.Cookies = append(state.Cookies, aistudio.StateCookie{Name: name, Value: value, Domain: ".google.com", Path: "/", Expires: -1, Secure: true})
	}
	if err := state.SetAuthExtension(aistudio.AuthExtension{Source: aistudio.AuthSource{Browser: "synthetic", Email: "synthetic@example.test"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return c.CredentialMaterial{NativeID: "synthetic@example.test", StorageState: raw}
}

func verificationInput() c.VerificationInput {
	return c.VerificationInput{Account: c.AccountRecord{Account: c.Account{AccountID: verificationAccountUUID, Index: 9, Versions: c.Versions{CredentialVersion: 2, StateVersion: 6}}, NativeID: "synthetic@example.test"}, Mode: c.VerificationModel, Model: "synthetic-model", Channels: []c.Channel{c.ChannelPlayground}}
}

type verificationSnapshot struct {
	record   c.AccountRecord
	material c.CredentialMaterial
	calls    atomic.Int32
}

func (s *verificationSnapshot) SnapshotCredentials(context.Context, string) (c.AccountRecord, c.CredentialMaterial, error) {
	s.calls.Add(1)
	return s.record, s.material, nil
}

type verificationWorker struct {
	state      aistudio.StorageState
	closeCount atomic.Int32
	prepare    func(context.Context, aistudio.ProtectedRequest) (aistudio.PreparedProtectedRequest, error)
	send       func(context.Context, aistudio.ProtectedRequest) (*aistudio.RPCResponse, error)
}

func (w *verificationWorker) Prepare(ctx context.Context, request aistudio.ProtectedRequest) (aistudio.PreparedProtectedRequest, error) {
	if w.prepare != nil {
		return w.prepare(ctx, request)
	}
	return aistudio.PreparedProtectedRequest{}, errors.New("fake worker: no real WAA")
}
func (w *verificationWorker) BrowserStorageState(context.Context) (aistudio.StorageState, error) {
	return w.state, nil
}
func (w *verificationWorker) SendProtected(ctx context.Context, request aistudio.ProtectedRequest) (*aistudio.RPCResponse, error) {
	if w.send != nil {
		return w.send(ctx, request)
	}
	return nil, errors.New("fake worker: no real network")
}
func (*verificationWorker) ProtocolHeaders(context.Context, string) (http.Header, error) {
	return http.Header{}, nil
}
func (w *verificationWorker) Close() error { w.closeCount.Add(1); return nil }

type verificationRPCFunc func(context.Context, aistudio.RPCRequest) (*aistudio.RPCResponse, error)

func (f verificationRPCFunc) Do(ctx context.Context, r aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
	return f(ctx, r)
}

func verificationResponse(status int, raw string) *aistudio.RPCResponse {
	return &aistudio.RPCResponse{StatusCode: status, Header: http.Header{"Content-Type": []string{aistudio.JSONProtobufContentType}}, Body: io.NopCloser(strings.NewReader(raw))}
}

func verificationModels(t *testing.T, model string) string {
	t.Helper()
	row := make([]any, 83)
	row[0] = "models/" + model
	row[3] = "Synthetic"
	row[5] = 4096
	row[6] = 1024
	row[7] = []string{"generateContent"}
	row[64] = []int{1}
	raw, err := json.Marshal([]any{[]any{row}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func verificationFrame(t *testing.T, text string, thought, finish bool) string {
	t.Helper()
	part := make([]any, 15)
	part[1] = text
	if thought {
		part[12] = true
	}
	candidate := []any{[]any{[]any{part}, "model"}, nil}
	if finish {
		candidate[1] = 1
	}
	frame := []any{[]any{candidate}}
	raw, err := json.Marshal([]any{[]any{frame}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type verificationHarness struct {
	target     *managementVerificationTarget
	store      *verificationSnapshot
	worker     *verificationWorker
	temp       string
	protected  atomic.Int32
	catalog    atomic.Int32
	root       string
	onGenerate func(context.Context, aistudio.GenerateRequest, aistudio.RPCRequest) (*aistudio.RPCResponse, error)
}

func newVerificationHarness(t *testing.T, input c.VerificationInput) *verificationHarness {
	t.Helper()
	material := verificationStorage(t, "original")
	store := &verificationSnapshot{record: input.Account, material: material}
	temp := t.TempDir()
	target, err := newManagementVerificationTarget(managementVerificationTargetOptions{Accounts: store, TempDir: temp, NewID: func(string) (string, error) { return verificationReqUUID, nil }, Environment: func(context.Context, string) (managementVerificationEnvironment, error) {
		return managementVerificationEnvironment{WAABackend: config.WAABackendGo, InitTimeout: time.Second}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := &verificationHarness{target: target.(*managementVerificationTarget), store: store, temp: temp}
	modelResponse := verificationModels(t, input.Model)
	generatedResponse := verificationFrame(t, "OK", false, true)
	h.target.worker = func(_ context.Context, id string, options camoufoxnative.Options) (managementVerificationWorker, error) {
		if id != input.Account.NativeID || options.ExecutablePath != "" || options.Model != input.Model || !options.TemporaryChat || !options.Headless {
			return nil, verificationCallbackError(t, errors.New("worker not isolated/pinned"))
		}
		if err := verificationPathWithin(temp, options.StorageStatePath); err != nil {
			return nil, verificationCallbackError(t, err)
		}
		state, err := aistudio.LoadStorageState(options.StorageStatePath)
		if err != nil {
			return nil, err
		}
		h.root = filepath.Dir(filepath.Dir(filepath.Dir(options.StorageStatePath)))
		h.worker = &verificationWorker{state: state}
		return h.worker, nil
	}
	h.target.identity = func(_ context.Context, state aistudio.StorageState, _ managementVerificationEnvironment) (string, int, error) {
		if len(state.Cookies) != 3 {
			return "", 0, verificationCallbackError(t, errors.New("wrong credential set"))
		}
		return input.Account.NativeID, 200, nil
	}
	h.onGenerate = func(_ context.Context, _ aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
		if rpc.Method == "GenerateContent" {
			return verificationResponse(200, generatedResponse), nil
		}
		return verificationResponse(200, verificationBuildFrame(false)), nil
	}
	h.target.protocol = func(pool *aistudio.AccountPool, _ managementVerificationWorker, _ managementVerificationEnvironment) (aistudio.RPCTransport, aistudio.ProtectedTransport, func(), error) {
		if pool.BuildEnabled() == pool.PlaygroundEnabled() {
			return nil, nil, nil, verificationCallbackError(t, errors.New("isolated pool must enable one channel"))
		}
		return verificationRPCFunc(func(_ context.Context, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
				h.catalog.Add(1)
				if rpc.AccountID != input.Account.NativeID || (pool.BuildEnabled() && rpc.Method != "ProxyUnaryCall") || (pool.PlaygroundEnabled() && rpc.Method != "ListModels") {
					return nil, verificationCallbackError(t, errors.New("wrong catalog request"))
				}
				if pool.BuildEnabled() {
					return verificationResponse(200, verificationBuildModels(input.Model)), nil
				}
				return verificationResponse(200, modelResponse), nil
			}), aistudio.ProtectedTransportFunc(func(ctx context.Context, request aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
				h.protected.Add(1)
				return h.onGenerate(ctx, request, rpc)
			}), func() {}, nil
	}
	return h
}

func (h *verificationHarness) assertReleased(t *testing.T) {
	t.Helper()
	if h.worker != nil && h.worker.closeCount.Load() != 1 {
		t.Fatal("worker not closed exactly once")
	}
	entries, err := os.ReadDir(h.temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary files/locks leaked: count=%d err=%v", len(entries), err)
	}
}

func TestManagementVerificationRealDecoderAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, text               string
		thought, finish, success bool
		status                   int
	}{{"visible", "OK", false, true, true, 200}, {"empty", "", false, true, false, 200}, {"thought-only", "internal", true, true, false, 200}, {"finish-only", "", false, true, false, 200}, {"no-terminal", "OK", false, false, false, 200}, {"upstream-error", "OK", false, true, false, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			input := verificationInput()
			candidate := verificationStorage(t, "candidate")
			input.Candidate = &candidate
			h := newVerificationHarness(t, input)
			generatedResponse := verificationFrame(t, tc.text, tc.thought, tc.finish)
			h.onGenerate = func(_ context.Context, request aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
				if request.AccountID != input.Account.NativeID || request.Model != input.Model || rpc.Method != "GenerateContent" || rpc.URL != aistudio.MakerSuiteRPCBase+"GenerateContent" || request.ID != verificationReqUUID {
					return nil, verificationCallbackError(t, errors.New("request not pinned"))
				}
				if h.worker.state.Cookies[0].Value != "candidate" || strings.Contains(string(h.store.material.StorageState), "candidate") {
					return nil, verificationCallbackError(t, errors.New("candidate crossed production boundary"))
				}
				return verificationResponse(tc.status, generatedResponse), nil
			}
			v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
			out, err := v.Verify(context.Background(), input, nil)
			if tc.success {
				if err != nil || !out.Result.Success || out.Result.Versions != input.Account.Versions || out.Result.ActualChannel != c.ChannelPlayground {
					t.Fatalf("result=%+v error=%v", out.Result, err)
				}
			} else if err == nil || out.Result.Success {
				t.Fatal("bad protocol output accepted")
			}
			if h.protected.Load() != 1 {
				t.Fatal("model call repeated")
			}
			h.assertReleased(t)
		})
	}
}

func TestManagementVerificationConnectionDoesNotGenerate(t *testing.T) {
	input := verificationInput()
	input.Mode = c.VerificationConnection
	h := newVerificationHarness(t, input)
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || out.Result.Stage != "connection_verified" || h.protected.Load() != 0 || h.catalog.Load() != 1 {
		t.Fatalf("connection error=%v stage=%s", err, out.Result.Stage)
	}
	h.assertReleased(t)
}

func TestManagementVerificationCandidateRefreshPrivate(t *testing.T) {
	input := verificationInput()
	candidate := verificationStorage(t, "candidate")
	input.Candidate = &candidate
	h := newVerificationHarness(t, input)
	original := string(candidate.StorageState)
	generatedResponse := verificationFrame(t, "OK", false, true)
	h.onGenerate = func(context.Context, aistudio.GenerateRequest, aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
		h.worker.state.Cookies[0].Value = "rotated-private"
		h.store.record.StateVersion++ // Simulates a later production disable; verifier must not publish over it.
		return verificationResponse(200, generatedResponse), nil
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || out.RefreshedCandidate == nil || out.Result.StateVersion != 6 || h.store.record.StateVersion != 7 || string(input.Candidate.StorageState) != original {
		t.Fatalf("candidate/version error=%v", err)
	}
	var returned aistudio.StorageState
	if json.Unmarshal(out.RefreshedCandidate.StorageState, &returned) != nil {
		t.Fatal("invalid private material")
	}
	extension, ok, err := returned.AuthExtension()
	if err != nil || !ok || extension.Source.Browser != "synthetic" || len(returned.Origins) != 1 || returned.Cookies[0].Value != "rotated-private" {
		t.Fatal("candidate lost extensions/origins")
	}
	if strings.Contains(string(h.store.material.StorageState), "rotated-private") {
		t.Fatal("production credentials modified")
	}
	h.assertReleased(t)
}

func TestManagementVerificationVersionIdentityAndMissingPlayground(t *testing.T) {
	for _, name := range []string{"version-conflict", "wrong-identity", "playground-fails-build-available", "open-cancel", "worker-failure"} {
		t.Run(name, func(t *testing.T) {
			input := verificationInput()
			h := newVerificationHarness(t, input)
			switch name {
			case "version-conflict":
				h.store.record.StateVersion++
			case "wrong-identity":
				h.target.identity = func(context.Context, aistudio.StorageState, managementVerificationEnvironment) (string, int, error) {
					return "other@example.test", 200, nil
				}
			case "playground-fails-build-available":
				buildResponse := verificationFrame(t, "Build OK", false, true)
				h.onGenerate = func(_ context.Context, _ aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
					// The synthetic upstream can serve Build successfully. A
					// Playground denial must still fail with exactly one send.
					if rpc.Method == "GenerateContent" {
						return verificationResponse(403, "[]"), nil
					}
					return verificationResponse(200, buildResponse), nil
				}
			case "worker-failure":
				h.target.worker = func(context.Context, string, camoufoxnative.Options) (managementVerificationWorker, error) {
					return nil, errors.New("synthetic startup failure")
				}
			}
			ctx := context.Background()
			if name == "open-cancel" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
			out, err := v.Verify(ctx, input, nil)
			if err == nil || out.Result.Success {
				t.Fatal("invalid verification succeeded")
			}
			if name != "playground-fails-build-available" && h.protected.Load() != 0 {
				t.Fatal("unexpected generation")
			}
			h.assertReleased(t)
		})
	}
}

func TestManagementVerificationCancelCloseReleasesStream(t *testing.T) {
	input := verificationInput()
	h := newVerificationHarness(t, input)
	opened := make(chan struct{})
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	h.onGenerate = func(context.Context, aistudio.GenerateRequest, aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
		close(opened)
		return &aistudio.RPCResponse{StatusCode: 200, Header: http.Header{"Content-Type": []string{aistudio.JSONProtobufContentType}}, Body: reader}, nil
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	verificationCtx, stopVerification := context.WithTimeout(context.Background(), verificationWaitTimeout)
	defer func() {
		stopVerification()
		_ = reader.Close()
		_ = writer.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = v.Close(cleanupCtx)
	}()
	done := make(chan error, 1)
	go func() { _, err := v.Verify(verificationCtx, input, nil); done <- err }()
	select {
	case <-opened:
	case err := <-done:
		t.Fatalf("verification returned before stream opened: %v", err)
	case <-verificationCtx.Done():
		t.Fatal("timed out waiting for stream to open")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := v.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled stream succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled verification")
	}
	written := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("[]")); written <- err }()
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("cancel did not close stream")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out checking cancelled stream closure")
	}
	if h.protected.Load() != 1 {
		t.Fatal("cancel replayed upstream")
	}
	h.assertReleased(t)
}

func TestManagementVerificationTransportRejectsOtherBranchesAndRepeats(t *testing.T) {
	input := verificationInput()
	var calls atomic.Int32
	newGuard := func() *managementVerificationTransport {
		return &managementVerificationTransport{accountID: input.Account.NativeID, model: input.Model, next: aistudio.ProtectedTransportFunc(func(context.Context, aistudio.GenerateRequest, aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
			calls.Add(1)
			return verificationResponse(200, "[]"), nil
		})}
	}
	tokens := int64(managementverify.ProbeMaxOutputTokens)
	request := aistudio.GenerateRequest{ID: verificationReqUUID, AccountID: input.Account.NativeID, Model: input.Model, Contents: []aistudio.Content{{Role: aistudio.RoleUser, Parts: []aistudio.Part{{Text: managementverify.ProbePrompt}}}}, Config: aistudio.GenerationConfig{MaxOutputTokens: &tokens}}
	body, err := aistudio.EncodeGenerateContentRequest(request, aistudio.GenerationDefaults{MaxOutputTokens: 1024}, aistudio.RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	rpc := aistudio.RPCRequest{Method: "GenerateContent", URL: aistudio.MakerSuiteRPCBase + "GenerateContent", AccountID: input.Account.NativeID, RequestID: verificationReqUUID, Body: body, Streaming: true}
	for _, mutate := range []func(*aistudio.RPCRequest){func(r *aistudio.RPCRequest) { r.Method = "ProxyStreamedCall" }, func(r *aistudio.RPCRequest) { r.URL = "https://example.test/GenerateContent" }, func(r *aistudio.RPCRequest) { r.AccountID = "other@example.test" }, func(r *aistudio.RPCRequest) { r.Body = []byte(`["models/other"]`) }} {
		r := rpc
		mutate(&r)
		if _, err := newGuard().DoProtected(context.Background(), request, r); err == nil {
			t.Fatal("unsafe branch accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected branch sent")
	}
	guard := newGuard()
	response, err := guard.DoProtected(context.Background(), request, rpc)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err := guard.DoProtected(context.Background(), request, rpc); err == nil {
		t.Fatal("repeat accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate upstream request")
	}
}

// Uses the real WorkerProtectedTransport, signer, temporary AccountLease and
// cookie persistence. Only worker proof/network and catalogue response are fake.
func TestManagementVerificationRealProtectedTransport(t *testing.T) {
	input := verificationInput()
	candidate := verificationStorage(t, "candidate")
	input.Candidate = &candidate
	h := newVerificationHarness(t, input)
	generatedResponse := verificationFrame(t, "OK", false, true)
	workerFactory := h.target.worker
	h.target.worker = func(ctx context.Context, id string, options camoufoxnative.Options) (managementVerificationWorker, error) {
		worker, err := workerFactory(ctx, id, options)
		if err != nil {
			return nil, err
		}
		h.worker.prepare = func(_ context.Context, request aistudio.ProtectedRequest) (aistudio.PreparedProtectedRequest, error) {
			if request.ProofField != 5 || request.Prompt != managementverify.ProbePrompt || request.URL != aistudio.MakerSuiteRPCBase+"GenerateContent" {
				return aistudio.PreparedProtectedRequest{}, verificationCallbackError(t, errors.New("wrong Playground binding"))
			}
			return aistudio.PreparedProtectedRequest{Body: request.Body, Headers: http.Header{"X-Goog-Authuser": []string{"0"}}}, nil
		}
		h.worker.send = func(_ context.Context, request aistudio.ProtectedRequest) (*aistudio.RPCResponse, error) {
			h.protected.Add(1)
			if !strings.Contains(request.Headers.Get("Authorization"), "SAPISID3PHASH") {
				return nil, verificationCallbackError(t, errors.New("real signer not used"))
			}
			h.worker.state.Cookies[0].Value = "rotated-in-fake-browser"
			return verificationResponse(200, generatedResponse), nil
		}
		return worker, nil
	}
	fakeProtocol := h.target.protocol
	h.target.protocol = func(pool *aistudio.AccountPool, worker managementVerificationWorker, env managementVerificationEnvironment) (aistudio.RPCTransport, aistudio.ProtectedTransport, func(), error) {
		fakeRPC, _, _, err := fakeProtocol(pool, worker, env)
		if err != nil {
			return nil, nil, nil, err
		}
		_, protected, cleanup, err := newManagementVerificationProtocol(pool, worker, env)
		return fakeRPC, protected, cleanup, err
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || !out.Result.Success || h.protected.Load() != 1 || out.RefreshedCandidate == nil {
		t.Fatalf("real protected stack: error=%v calls=%d", err, h.protected.Load())
	}
	if strings.Contains(string(h.store.material.StorageState), "rotated-in-fake-browser") {
		t.Fatal("isolated cookie persistence reached production")
	}
	h.assertReleased(t)
}

func TestManagementVerificationCloseDuringOpen(t *testing.T) {
	input := verificationInput()
	h := newVerificationHarness(t, input)
	opening := make(chan struct{})
	h.target.worker = func(ctx context.Context, _ string, _ camoufoxnative.Options) (managementVerificationWorker, error) {
		close(opening)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	verificationCtx, stopVerification := context.WithTimeout(context.Background(), verificationWaitTimeout)
	defer func() {
		stopVerification()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = v.Close(cleanupCtx)
	}()
	done := make(chan error, 1)
	go func() { _, err := v.Verify(verificationCtx, input, nil); done <- err }()
	select {
	case <-opening:
	case err := <-done:
		t.Fatalf("verification returned before worker opening: %v", err)
	case <-verificationCtx.Done():
		t.Fatal("timed out waiting for worker opening")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := v.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled opening succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled opening")
	}
	h.assertReleased(t)
}

func TestManagementVerificationPartialWorkerFailureAndSingleRun(t *testing.T) {
	t.Run("partial-worker", func(t *testing.T) {
		input := verificationInput()
		h := newVerificationHarness(t, input)
		factory := h.target.worker
		h.target.worker = func(ctx context.Context, id string, options camoufoxnative.Options) (managementVerificationWorker, error) {
			worker, err := factory(ctx, id, options)
			if err != nil {
				return worker, err
			}
			return worker, errors.New("synthetic partial startup failure")
		}
		v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
		if _, err := v.Verify(context.Background(), input, nil); err == nil {
			t.Fatal("partial startup succeeded")
		}
		h.assertReleased(t)
	})
	t.Run("single-run", func(t *testing.T) {
		input := verificationInput()
		h := newVerificationHarness(t, input)
		session, err := h.target.Open(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		probe := c.VerificationProbe{Mode: input.Mode, Model: input.Model, Prompt: managementverify.ProbePrompt, MaxOutputTokens: managementverify.ProbeMaxOutputTokens}
		if _, err := session.Run(context.Background(), probe, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Run(context.Background(), probe, nil); err == nil {
			t.Fatal("session repeated model request")
		}
		if h.protected.Load() != 1 {
			t.Fatal("generation repeated")
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		h.assertReleased(t)
	})
}

func TestManagementVerificationFixturePathContainment(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		root   string
		path   string
		inside bool
	}{
		{"mixed-separators", filepath.ToSlash(root), filepath.Join(root, "account", "storage-state.json"), true},
		{"slash-child", root, filepath.ToSlash(filepath.Join(root, "account", "storage-state.json")), true},
		{"normalized-components", root, filepath.Join(root, "account", "..", "other", "storage-state.json"), true},
		{"parent-escape", root, filepath.Join(root, "..", "outside", "storage-state.json"), false},
		{"prefix-sibling", root, filepath.Join(root+"-sibling", "storage-state.json"), false},
		{"root-itself", root, root, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verificationPathWithin(tc.root, tc.path)
			if (err == nil) != tc.inside {
				t.Fatalf("containment inside=%t error=%v", tc.inside, err)
			}
		})
	}
}

// Build upstream fixtures use the real proxy envelope and decoder, rather than
// returning a verification result directly.
func verificationBuildModels(model string) string {
	raw, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"name": "models/" + model, "inputTokenLimit": 4096, "outputTokenLimit": 1024, "supportedGenerationMethods": []string{"generateContent"}}}})
	envelope, _ := json.Marshal([]any{string(raw)})
	return string(envelope)
}
func verificationBuildFrame(unary bool) string {
	raw := `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}`
	var envelope any = []any{[]any{[]any{raw}}}
	if unary {
		envelope = []any{raw}
	}
	encoded, _ := json.Marshal(envelope)
	return string(encoded)
}

func TestManagementVerificationSelectableChannelsRealProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels []c.Channel
		mode     c.VerificationMode
		want     []c.Channel
	}{
		{"default-build", nil, c.VerificationModel, []c.Channel{c.ChannelBuild}},
		{"playground", []c.Channel{c.ChannelPlayground}, c.VerificationModel, []c.Channel{c.ChannelPlayground}},
		{"both-canonical", []c.Channel{c.ChannelPlayground, c.ChannelBuild}, c.VerificationModel, []c.Channel{c.ChannelBuild, c.ChannelPlayground}},
		{"build-connection", nil, c.VerificationConnection, []c.Channel{c.ChannelBuild}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := verificationInput()
			input.Channels, input.Mode = tc.channels, tc.mode
			h := newVerificationHarness(t, input)
			v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
			out, err := v.Verify(context.Background(), input, nil)
			if err != nil || !c.ValidVerificationResult(out.Result) || !out.Result.Success || len(out.Result.ChannelResults) != len(tc.want) {
				t.Fatalf("result=%+v err=%v", out.Result, err)
			}
			for i, channel := range tc.want {
				child := out.Result.ChannelResults[i]
				if child.RequestedChannel != channel || child.ActualChannel != channel {
					t.Fatalf("wrong channel evidence: %+v", child)
				}
			}
			if len(tc.want) == 2 && (out.Result.ActualChannel != "" || out.Result.RequestID != "") {
				t.Fatal("aggregate fabricated a single transport")
			}
			wantCalls := int32(len(tc.want))
			if tc.mode == c.VerificationConnection {
				wantCalls = 0
			}
			if h.protected.Load() != wantCalls || h.catalog.Load() != int32(len(tc.want)) {
				t.Fatalf("generation=%d catalog=%d", h.protected.Load(), h.catalog.Load())
			}
			h.assertReleased(t)
		})
	}
}

func TestManagementVerificationSelectedChannelNeverFallsBack(t *testing.T) {
	for _, channel := range []c.Channel{c.ChannelBuild, c.ChannelPlayground} {
		t.Run(string(channel), func(t *testing.T) {
			input := verificationInput()
			input.Channels = []c.Channel{channel}
			h := newVerificationHarness(t, input)
			var methods []string
			h.onGenerate = func(_ context.Context, _ aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
				methods = append(methods, rpc.Method)
				return verificationResponse(403, "[]"), nil
			}
			v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
			out, err := v.Verify(context.Background(), input, nil)
			if err == nil || out.Result.Success || !c.ValidVerificationResult(out.Result) || len(methods) != 1 {
				t.Fatalf("result=%+v methods=%v err=%v", out.Result, methods, err)
			}
			if (channel == c.ChannelBuild && methods[0] != "ProxyStreamedCall" && methods[0] != "ProxyUnaryCall") || (channel == c.ChannelPlayground && methods[0] != "GenerateContent") {
				t.Fatal("cross-channel fallback", methods)
			}
			if out.Result.ChannelResults[0].ActualChannel != channel || *out.Result.ChannelResults[0].UpstreamStatus != 403 {
				t.Fatal("lost failure evidence")
			}
			h.assertReleased(t)
		})
	}
}

func TestManagementVerificationBothPreservesCompletedEvidenceOnCancel(t *testing.T) {
	input := verificationInput()
	input.Channels = []c.Channel{c.ChannelBuild, c.ChannelPlayground}
	h := newVerificationHarness(t, input)
	opened := make(chan struct{})
	reader, writer := io.Pipe()
	defer writer.Close()
	h.onGenerate = func(_ context.Context, _ aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
		if rpc.Method == "GenerateContent" {
			close(opened)
			return &aistudio.RPCResponse{StatusCode: 200, Header: http.Header{}, Body: reader}, nil
		}
		return verificationResponse(200, verificationBuildFrame(rpc.Method == "ProxyUnaryCall")), nil
	}
	checkpoints := make(chan c.VerificationResult, 2)
	input.Checkpoint = func(result c.VerificationResult) error { checkpoints <- result; return nil }
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	ctx, cancel := context.WithTimeout(context.Background(), verificationWaitTimeout)
	defer cancel()
	type answer struct {
		out c.VerificationOutput
		err error
	}
	done := make(chan answer, 1)
	go func() { out, err := v.Verify(ctx, input, nil); done <- answer{out, err} }()
	select {
	case <-opened:
	case a := <-done:
		t.Fatalf("early return: %v", a.err)
	case <-ctx.Done():
		t.Fatal("second channel never opened")
	}
	select {
	case first := <-checkpoints:
		if first.Success || len(first.ChannelResults) != 1 || !first.ChannelResults[0].Success {
			t.Fatalf("bad durable checkpoint: %+v", first)
		}
	default:
		t.Fatal("first channel was not checkpointed before second started")
	}
	cancel()
	select {
	case a := <-done:
		if a.err == nil || a.out.Result.Success || len(a.out.Result.ChannelResults) != 2 || !a.out.Result.ChannelResults[0].Success || a.out.Result.ChannelResults[1].Success || !c.ValidVerificationResult(a.out.Result) {
			t.Fatalf("lost completed evidence: %+v %v", a.out.Result, a.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not release stream")
	}
	h.assertReleased(t)
}

func TestManagementVerificationBothVersionConflictDoesNotGenerateSuccessor(t *testing.T) {
	input := verificationInput()
	input.Channels = []c.Channel{c.ChannelBuild, c.ChannelPlayground}
	h := newVerificationHarness(t, input)
	input.Checkpoint = func(result c.VerificationResult) error {
		if len(result.ChannelResults) == 1 {
			h.store.record.StateVersion++
		}
		return nil
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if c.PublicError(err).Code != c.CodeVersionConflict || out.Result.Success || h.protected.Load() != 1 || len(out.Result.ChannelResults) != 2 || !out.Result.ChannelResults[0].Success {
		t.Fatalf("conflict replayed: %+v %v calls=%d", out.Result, err, h.protected.Load())
	}
	h.assertReleased(t)
}

func TestManagementVerificationBuildRealProtectedTransport(t *testing.T) {
	input := verificationInput()
	input.Channels = []c.Channel{c.ChannelBuild}
	h := newVerificationHarness(t, input)
	workerFactory := h.target.worker
	h.target.worker = func(ctx context.Context, id string, options camoufoxnative.Options) (managementVerificationWorker, error) {
		worker, err := workerFactory(ctx, id, options)
		if err != nil {
			return nil, err
		}
		h.worker.prepare = func(_ context.Context, request aistudio.ProtectedRequest) (aistudio.PreparedProtectedRequest, error) {
			if request.ProofField != 3 || request.URL != aistudio.MakerSuiteRPCBase+"ProxyStreamedCall" || !strings.HasPrefix(request.Prompt, "/v1beta/models/"+input.Model+":streamGenerateContent ") {
				return aistudio.PreparedProtectedRequest{}, errors.New("Build proof bound to wrong entry")
			}
			return aistudio.PreparedProtectedRequest{Body: request.Body, Headers: http.Header{"X-Goog-Authuser": []string{"0"}}}, nil
		}
		h.worker.send = func(_ context.Context, request aistudio.ProtectedRequest) (*aistudio.RPCResponse, error) {
			h.protected.Add(1)
			if !strings.Contains(request.Headers.Get("Authorization"), "SAPISID3PHASH") {
				return nil, errors.New("real signer not used")
			}
			return verificationResponse(200, verificationBuildFrame(false)), nil
		}
		return worker, nil
	}
	fakeProtocol := h.target.protocol
	h.target.protocol = func(pool *aistudio.AccountPool, worker managementVerificationWorker, env managementVerificationEnvironment) (aistudio.RPCTransport, aistudio.ProtectedTransport, func(), error) {
		rpc, _, _, err := fakeProtocol(pool, worker, env)
		if err != nil {
			return nil, nil, nil, err
		}
		_, protected, closeProtocol, err := newManagementVerificationProtocol(pool, worker, env)
		return rpc, protected, closeProtocol, err
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || !out.Result.Success || out.Result.ActualChannel != c.ChannelBuild || h.protected.Load() != 1 {
		t.Fatalf("Build protected stack: %+v %v calls=%d", out.Result, err, h.protected.Load())
	}
	h.assertReleased(t)
}

func TestManagementVerificationBothCandidateRefreshStaysPrivate(t *testing.T) {
	input := verificationInput()
	input.Channels = []c.Channel{c.ChannelBuild, c.ChannelPlayground}
	candidate := verificationStorage(t, "candidate-private")
	input.Candidate = &candidate
	original := string(candidate.StorageState)
	h := newVerificationHarness(t, input)
	h.onGenerate = func(_ context.Context, _ aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
		if rpc.Method == "GenerateContent" {
			if h.worker.state.Cookies[0].Value != "build-private-refresh" {
				return nil, errors.New("second isolated channel lost candidate lineage")
			}
			h.worker.state.Cookies[0].Value = "playground-private-refresh"
			return verificationResponse(200, verificationFrame(t, "OK", false, true)), nil
		}
		h.worker.state.Cookies[0].Value = "build-private-refresh"
		return verificationResponse(200, verificationBuildFrame(false)), nil
	}
	v, _ := managementverify.New(c.VerifierOptions{Target: h.target})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || !out.Result.Success || out.RefreshedCandidate == nil || !strings.Contains(string(out.RefreshedCandidate.StorageState), "playground-private-refresh") || string(candidate.StorageState) != original || strings.Contains(string(h.store.material.StorageState), "private-refresh") {
		t.Fatalf("candidate lineage failed: %v", err)
	}
	for _, child := range out.Result.ChannelResults {
		if child.Versions != input.Account.Versions {
			t.Fatal("candidate evidence version rewritten")
		}
	}
	h.assertReleased(t)
}
