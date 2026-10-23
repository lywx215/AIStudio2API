package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type errorClock struct{ nanos atomic.Int64 }

func newErrorClock() *errorClock {
	c := &errorClock{}
	c.nanos.Store(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *errorClock) now() time.Time              { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *errorClock) advance(value time.Duration) { c.nanos.Add(int64(value)) }
func discardEvent(contract.Event) error           { return nil }
func errorSelection() contract.Selection          { return contract.Selection{ModelID: "observed"} }
func errorAttachmentRequest() contract.Request {
	input := request()
	input.Attachments = []contract.Attachment{{Filename: "synthetic.txt", MIMEType: "text/plain", Data: []byte("synthetic")}}
	return input
}

func stageTransport(action string, fail func(*http.Request) (*http.Response, error)) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/"+action) {
			return fail(r)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getoxsrf"):
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		case strings.HasSuffix(r.URL.Path, "/widgetCreateSession"):
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/widgetAddContextFile"):
			return reply(200, `{"addContextFileResponse":{"fileId":"synthetic-file"}}`), nil
		default:
			return reply(200, stream), nil
		}
	}
}

func classifiedEngine(t *testing.T, transport http.RoundTripper) (*Engine, *errorClock) {
	t.Helper()
	e := New(context.Background(), []vault.Account{account()}, 1, time.Minute, transport)
	clock := newErrorClock()
	e.policy = backoffPolicy{now: clock.now, random: func() float64 { return 1 }}
	t.Cleanup(e.Close)
	return e, clock
}

func requireOperation(t *testing.T, err error, kind ErrorKind, stage ErrorStage, code int) *OperationError {
	t.Helper()
	var operation *OperationError
	if !errors.As(err, &operation) || operation.Kind != kind || operation.Stage != stage {
		t.Fatalf("expected %s/%s, got %#v (%v)", kind, stage, operation, err)
	}
	requireFailure(t, err, code)
	return operation
}

func TestOperationClassificationStagesAndRPCEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name, action, body string
		httpStatus         int
		kind               ErrorKind
		stage              ErrorStage
		publicCode         int
		state              string
		submitted, output  bool
		failures           int
		status, reason     string
	}{
		{"auth401", "getoxsrf", `{}`, 401, ErrorAuthentication, StageAuth, 503, "auth_required", false, false, 0, "UNAUTHENTICATED", ""},
		{"parameter400", "widgetStreamAssist", `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"synthetic-secret"}}`, 400, ErrorUpstreamRejected, StageGenerate, 400, "ready", true, false, 0, "INVALID_ARGUMENT", ""},
		{"permission403", "widgetStreamAssist", `{"error":{"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_ACCESS_DENIED","metadata":{"token":"synthetic-secret"}}]}}`, 403, ErrorPermissionDenied, StageGenerate, 403, "ready", true, false, 0, "PERMISSION_DENIED", "MODEL_ACCESS_DENIED"},
		{"generic403", "widgetStreamAssist", `{"message":"synthetic-secret"}`, 403, ErrorPermissionDenied, StageGenerate, 403, "ready", true, false, 0, "PERMISSION_DENIED", ""},
		{"unauthenticated403", "widgetStreamAssist", `{"error":{"status":"UNAUTHENTICATED"}}`, 403, ErrorAuthentication, StageGenerate, 503, "auth_required", true, false, 0, "UNAUTHENTICATED", ""},
		{"expired403", "widgetStreamAssist", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCESS_TOKEN_EXPIRED"}]}}`, 403, ErrorAuthentication, StageGenerate, 503, "auth_required", true, false, 0, "PERMISSION_DENIED", "ACCESS_TOKEN_EXPIRED"},
		{"quota429", "widgetStreamAssist", `{"error":{"status":"RESOURCE_EXHAUSTED"}}`, 429, ErrorQuotaExhausted, StageGenerate, 429, "cooldown", true, false, 1, "RESOURCE_EXHAUSTED", ""},
		{"server503", "widgetCreateSession", `{}`, 503, ErrorTransient, StageSession, 502, "cooldown", false, false, 1, "UNAVAILABLE", ""},
		{"sessionRPC200", "widgetCreateSession", `[{"error":{"code":16,"message":"synthetic-secret"}}]`, 200, ErrorAuthentication, StageSession, 503, "auth_required", false, false, 0, "UNAUTHENTICATED", ""},
		{"streamRPC200", "widgetStreamAssist", `{"error":{"code":8,"message":"synthetic-secret"}}`, 200, ErrorQuotaExhausted, StageStream, 429, "cooldown", true, false, 1, "RESOURCE_EXHAUSTED", ""},
		{"streamPartialRPC", "widgetStreamAssist", `[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"text":"partial"}}}]}}},{"error":{"code":7}}]`, 200, ErrorPermissionDenied, StageStream, 403, "ready", true, true, 0, "PERMISSION_DENIED", ""},
		{"unknownReason", "widgetStreamAssist", `{"error":{"status":"synthetic-secret","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"synthetic-secret"}]}}`, 403, ErrorPermissionDenied, StageGenerate, 403, "ready", true, false, 0, "PERMISSION_DENIED", ""},
		{"sessionJSON", "widgetCreateSession", `{`, 200, ErrorProtocol, StageSession, 502, "cooldown", false, false, 1, "", ""},
		{"uploadJSON", "widgetAddContextFile", `{`, 200, ErrorProtocol, StageUpload, 502, "cooldown", false, false, 1, "", ""},
		{"unknownRPC", "widgetStreamAssist", `{"error":{"message":"synthetic-secret"}}`, 200, ErrorProtocol, StageStream, 502, "cooldown", true, false, 1, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			e, _ := classifiedEngine(t, stageTransport(tc.action, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return reply(tc.httpStatus, tc.body), nil
			}))
			input := request()
			if tc.stage == StageUpload {
				input = errorAttachmentRequest()
			}
			err := e.Generate(context.Background(), errorSelection(), input, discardEvent)
			operation := requireOperation(t, err, tc.kind, tc.stage, tc.publicCode)
			if operation.Submitted != tc.submitted || operation.OutputStarted != tc.output || operation.Status != tc.status || operation.Reason != tc.reason {
				t.Fatal("incorrect safe operation metadata", operation)
			}
			if tc.httpStatus != 200 || strings.Contains(tc.name, "RPC") && tc.status != "" {
				if operation.HTTPStatus != tc.httpStatus {
					t.Fatal("RPC classification replaced actual HTTP status", operation.HTTPStatus)
				}
			}
			state := e.Snapshot().Accounts[0]
			if state.State != tc.state || state.ConsecutiveFailures != tc.failures || state.LastErrorKind != tc.kind || state.LastErrorStage != tc.stage || calls.Load() != 1 {
				t.Fatal("wrong account outcome or retried an operation", state, calls.Load())
			}
			encoded, _ := json.Marshal([]any{operation, state})
			if strings.Contains(string(encoded), "synthetic-secret") || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("unsafe upstream body escaped")
			}
		})
	}
}

type failedBody struct {
	prefix *strings.Reader
	cause  error
}

func (b *failedBody) Read(data []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(data)
	}
	return 0, b.cause
}
func (*failedBody) Close() error { return nil }

func TestReadFailuresAfterHTTP200RetainNetworkAndTimeoutCause(t *testing.T) {
	for _, target := range []struct {
		action, prefix string
		stage          ErrorStage
	}{
		{"getoxsrf", `{"keyId":`, StageAuth},
		{"widgetCreateSession", `{"session":`, StageSession},
		{"widgetAddContextFile", `{"addContextFileResponse":`, StageUpload},
		{"widgetStreamAssist", `[{"streamAssistResponse":`, StageStream},
		{"widgetStreamAssist", stream, StageStream}, // failure while checking bytes after ]
	} {
		for _, cause := range []error{context.DeadlineExceeded, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("synthetic-reset")}, errors.New("synthetic-reader-failure")} {
			t.Run(target.action+"/"+target.prefix+"/"+cause.Error(), func(t *testing.T) {
				e, _ := classifiedEngine(t, stageTransport(target.action, func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &failedBody{strings.NewReader(target.prefix), cause}}, nil
				}))
				input := request()
				if target.stage == StageUpload {
					input = errorAttachmentRequest()
				}
				finished := 0
				err := e.Generate(context.Background(), errorSelection(), input, func(event contract.Event) error {
					if event.Finished {
						finished++
					}
					return nil
				})
				code := 502
				if errors.Is(cause, context.DeadlineExceeded) {
					code = 504
				}
				requireOperation(t, err, ErrorTransient, target.stage, code)
				if !errors.Is(err, cause) || finished != 0 || e.Snapshot().Accounts[0].ConsecutiveFailures != 1 {
					t.Fatal("read cause, terminal or account classification lost", err, finished, e.Snapshot())
				}
				var network net.Error
				if errors.As(cause, &network) && !errors.As(err, &network) {
					t.Fatal("typed network cause lost through public operation error")
				}
			})
		}
	}
}

func TestDownstreamFailureAndCallerCancellationDoNotPenalizeAccounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		cause  error
		kind   ErrorKind
	}{
		{"write", false, errors.New("synthetic-sink-error"), ErrorClientWrite},
		{"writeTimeout", false, context.DeadlineExceeded, ErrorClientWrite},
		{"writeSyntax", false, &json.SyntaxError{}, ErrorClientWrite},
		{"localBudget", false, contract.Fail(429, "RESOURCE_EXHAUSTED", "Synthetic budget exhausted"), ErrorLocalResource},
		{"cancel", true, context.Canceled, ErrorClientCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := classifiedEngine(t, stageTransport("none", nil))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := e.Generate(ctx, errorSelection(), request(), func(event contract.Event) error {
				if len(event.Parts) > 0 {
					if tc.cancel {
						cancel()
					}
					return tc.cause
				}
				return nil
			})
			code := 499
			if tc.kind == ErrorLocalResource {
				code = 429
			}
			operation := requireOperation(t, err, tc.kind, StageStream, code)
			state := e.Snapshot().Accounts[0]
			if !operation.OutputStarted || !operation.Submitted || state.State != "ready" || state.ConsecutiveFailures != 0 || !state.CooldownUntil.IsZero() || len(state.VerifiedModels) != 0 {
				t.Fatal("client/local error penalized account or verified model", operation, state)
			}
		})
	}
}

func TestBadSessionPathsCannotReachUploadOrGeneration(t *testing.T) {
	for _, path := range []string{"", "https://example.invalid/sessions/x", "sessions/../x", "sessions/%2e%2e", "sessions/x?token=synthetic", "sessions/x#fragment", "sessions//x", "sessions/x\\y", "sessions/x\n", "other/x"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			e, _ := classifiedEngine(t, stageTransport("widgetCreateSession", func(*http.Request) (*http.Response, error) {
				calls++
				data, _ := json.Marshal(map[string]any{"session": map[string]string{"name": path}})
				return reply(200, string(data)), nil
			}))
			err := e.Generate(context.Background(), errorSelection(), request(), discardEvent)
			operation := requireOperation(t, err, ErrorProtocol, StageSession, 502)
			if operation.Submitted || calls != 1 {
				t.Fatal("invalid session submitted generation")
			}
		})
	}
}

func TestRetryAfterAndExponentialBackoffAreBounded(t *testing.T) {
	now := newErrorClock().now()
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0}, {"120", 2 * time.Minute}, {"0", 0}, {"-5", 0}, {"1.5", 0},
		{"9999999999999999999999999999", 24 * time.Hour}, {"86401", 24 * time.Hour},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(48 * time.Hour).Format(http.TimeFormat), 24 * time.Hour},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0}, {"synthetic-token", 0},
	} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Fatalf("Retry-After %q: %v != %v", tc.value, got, tc.want)
		}
	}
	for _, tc := range []struct {
		kind     ErrorKind
		failures int
		random   float64
		server   time.Duration
		want     time.Duration
	}{
		{ErrorTransient, 1, 0, 0, time.Second}, {ErrorTransient, 1, 1, 0, 2 * time.Second},
		{ErrorTransient, 3, 1, 0, 8 * time.Second}, {ErrorQuotaExhausted, 1, 1, 0, 15 * time.Second},
		{ErrorTransient, 100, 1, 0, 5 * time.Minute}, {ErrorTransient, 100, 0, 0, 150 * time.Second},
		{ErrorTransient, 1, math.NaN(), 0, time.Second}, {ErrorTransient, 1, 2, 0, 2 * time.Second},
		{ErrorTransient, 1, 1, time.Hour, time.Hour}, {ErrorQuotaExhausted, 1, 1, 48 * time.Hour, 24 * time.Hour},
	} {
		policy := backoffPolicy{random: func() float64 { return tc.random }}
		if got := policy.delay(tc.kind, tc.failures, tc.server); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestErrorHistoryBackoffMetadataSuspendAndCredentialEpoch(t *testing.T) {
	var mode atomic.Int32
	e, clock := classifiedEngine(t, stageTransport("widgetStreamAssist", func(*http.Request) (*http.Response, error) {
		switch mode.Load() {
		case 1:
			response := reply(429, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`)
			response.Header.Set("Retry-After", "3600")
			return response, nil
		case 2:
			return reply(401, `{}`), nil
		default:
			return reply(200, stream), nil
		}
	}))
	generate := func() error { return e.Generate(context.Background(), errorSelection(), request(), discardEvent) }
	if err := generate(); err != nil {
		t.Fatal(err)
	}
	verified := e.Snapshot().Accounts[0]
	mode.Store(1)
	requireOperation(t, generate(), ErrorQuotaExhausted, StageGenerate, 429)
	before := e.Snapshot().Accounts[0]
	if before.CooldownUntil != clock.now().Add(time.Hour) || before.ConsecutiveFailures != 1 {
		t.Fatal("Retry-After floor lost", before)
	}
	value := account()
	value.Label = "new metadata"
	e.Reconcile([]vault.Account{value}, 2, 2*time.Minute)
	e.Configure(3, 3*time.Minute)
	e.Suspend()
	e.Reconcile([]vault.Account{value}, 1, time.Minute)
	after := e.Snapshot().Accounts[0]
	if after.CooldownUntil != before.CooldownUntil || after.LastErrorAt != before.LastErrorAt || after.ConsecutiveFailures != 1 || after.VerifiedModels["observed"] != verified.VerifiedModels["observed"] {
		t.Fatal("metadata/off-on reset error or verification history", after)
	}
	clock.advance(time.Hour)
	mode.Store(0)
	if err := generate(); err != nil {
		t.Fatal(err)
	}
	reset := e.Snapshot().Accounts[0]
	if reset.ConsecutiveFailures != 0 || !reset.CooldownUntil.IsZero() || reset.LastErrorKind != "" || !reset.LastErrorAt.IsZero() {
		t.Fatal("successful terminal did not reset backoff/error", reset)
	}
	verified = reset
	mode.Store(2)
	requireOperation(t, generate(), ErrorAuthentication, StageGenerate, 503)
	invalid := e.Snapshot().Accounts[0]
	if invalid.AuthVerified || invalid.AuthVerifiedAt != verified.AuthVerifiedAt || invalid.VerifiedModels["observed"] != verified.VerifiedModels["observed"] {
		t.Fatal("401 erased same-credential historical authentication/model evidence", invalid)
	}
	value.Epoch++
	e.Reconcile([]vault.Account{value}, 1, time.Minute)
	cleared := e.Snapshot().Accounts[0]
	if cleared.AuthVerified || !cleared.AuthVerifiedAt.IsZero() || len(cleared.VerifiedModels) != 0 || cleared.LastErrorKind != "" || cleared.ConsecutiveFailures != 0 || !cleared.CooldownUntil.IsZero() {
		t.Fatal("credential epoch retained old evidence", cleared)
	}
}

func TestNonAccountFailuresDoNotIncrementOrResetPreviousTransientCount(t *testing.T) {
	e, clock := classifiedEngine(t, stageTransport("none", nil))
	item := mustLease(t, e, account().ID)
	e.release(item, classifyOperation(&transportError{io.ErrUnexpectedEOF}, StageSession, false, false, nil))
	clock.advance(3 * time.Second)
	for _, failure := range []error{
		contract.Fail(400, "INVALID_ARGUMENT", "Synthetic bad parameter"),
		contract.Fail(413, "RESOURCE_EXHAUSTED", "Synthetic budget exhausted"),
		&responseError{status: 400}, &responseError{status: 403},
		context.Canceled, &deliveryError{errors.New("synthetic-sink")},
	} {
		item := mustLease(t, e, account().ID)
		e.release(item, classifyOperation(failure, StageGenerate, false, false, nil))
		state := e.Snapshot().Accounts[0]
		if state.State != "ready" || state.ConsecutiveFailures != 1 {
			t.Fatal("non-account failure altered backoff count", state)
		}
	}
	item = mustLease(t, e, account().ID)
	e.release(item, nil)
	if e.Snapshot().Accounts[0].ConsecutiveFailures != 0 {
		t.Fatal("success did not clear previous failures")
	}
}

func TestBoundedErrorMetadataCannotRetainBodySecrets(t *testing.T) {
	response := reply(403, `{"error":{"status":"PERMISSION_DENIED","message":"synthetic-secret","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"synthetic-secret"}]}}`)
	operation := classifyOperation(httpFailure(response, time.Now()), StageGenerate, true, false, nil)
	encoded, _ := json.Marshal(operation)
	if operation.Reason != "" || strings.Contains(operation.Error(), "synthetic-secret") || strings.Contains(string(encoded), "synthetic-secret") {
		t.Fatal("unknown reason/message retained", string(encoded))
	}
	response = reply(403, strings.Repeat(" ", maxErrorBody)+`{"error":{"status":"UNAUTHENTICATED"}}`)
	operation = classifyOperation(httpFailure(response, time.Now()), StageGenerate, true, false, nil)
	if operation.Kind != ErrorPermissionDenied || operation.Status != "PERMISSION_DENIED" {
		t.Fatal("oversize body changed trusted HTTP classification", operation)
	}
}

func TestInvalidGenerationJSONIsLocalAndRejectedBeforeLease(t *testing.T) {
	var calls atomic.Int32
	e, _ := classifiedEngine(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not authenticate or submit invalid parameters")
	}))
	input := request()
	input.Generation["temperature"] = json.RawMessage(`synthetic-invalid`)
	operation := requireOperation(t, e.Generate(context.Background(), errorSelection(), input, discardEvent), ErrorLocalParameter, StagePreflight, 400)
	state := e.Snapshot().Accounts[0]
	if calls.Load() != 0 || operation.Submitted || state.State != "ready" || state.ConsecutiveFailures != 0 || state.LastErrorKind != "" {
		t.Fatal("preflight parameter failure touched an account", state, operation)
	}
}
