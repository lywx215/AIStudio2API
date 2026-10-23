package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

const privateErrorDetail = "AI Studio Build playground internal-backend-model-947 RPC internal.example.invalid"

func assertPrivateErrorAbsent(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"AI Studio", "Build", "Playground", "internal-backend-model-947", "ProxyUnaryCall", "ProxyStreamedCall", "internal.example.invalid"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("response exposed internal marker %q: %s", forbidden, body)
		}
	}
}

func TestErrorPresentationPreservesInternalDiagnostics(t *testing.T) {
	err := &aistudio.RPCError{Method: "ProxyUnaryCall", StatusCode: 400, Message: privateErrorDetail}
	for name, write := range map[string]func(http.ResponseWriter){
		"openai":     func(w http.ResponseWriter) { writeOpenAIRequestError(w, err) },
		"anthropic":  func(w http.ResponseWriter) { writeAnthropicRequestError(w, err) },
		"gemini":     func(w http.ResponseWriter) { writeGeminiRequestError(w, err) },
		"admin":      func(w http.ResponseWriter) { writeAdminUpstreamError(w, err) },
		"validation": func(w http.ResponseWriter) { writeOpenAIError(w, 400, "invalid_request", privateErrorDetail) },
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			metadata := &accessLogMetadata{}
			write(&accessLogResponseWriter{ResponseWriter: recorder, metadata: metadata})
			if recorder.Code != 400 {
				t.Fatalf("status=%d", recorder.Code)
			}
			assertPrivateErrorAbsent(t, recorder.Body.String())
			if !strings.Contains(metadata.err, privateErrorDetail) {
				t.Fatal("original internal diagnostic was lost")
			}
		})
	}
}

type presentationService struct {
	aistudio.Service
	err error
}

func (s presentationService) Generate(context.Context, aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	events := make(chan aistudio.Event, 2)
	events <- aistudio.Event{Kind: aistudio.EventText, Text: "Partial answer"}
	events <- aistudio.Event{Kind: aistudio.EventError, Err: s.err}
	close(events)
	return events, nil
}

func (s presentationService) Embed(context.Context, aistudio.EmbeddingRequest) (aistudio.EmbeddingResult, error) {
	return aistudio.EmbeddingResult{}, s.err
}

func TestEmbeddingErrorPresentation(t *testing.T) {
	handler := NewHandler(presentationService{err: &aistudio.RPCError{Method: "ProxyUnaryCall", StatusCode: 400, Message: privateErrorDetail}}, Config{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"public-alias","input":"hello"}`)))
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), clientErrorMessage(400, "upstream_error")) {
		t.Fatalf("embedding response=%d %s", recorder.Code, recorder.Body)
	}
	assertPrivateErrorAbsent(t, recorder.Body.String())
}

func TestStreamingErrorPresentation(t *testing.T) {
	for _, test := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"public-alias","messages":[{"role":"user","content":"hello"}],"stream":true}`},
		{"responses", "/v1/responses", `{"model":"public-alias","input":"hello","stream":true}`},
		{"anthropic", "/v1/messages", `{"model":"public-alias","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"stream":true}`},
		{"gemini", "/v1beta/models/public-alias:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hello"}]}]}`},
		{"interactions", "/v1/interactions", `{"model":"public-alias","input":"hello","stream":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(presentationService{err: &aistudio.RPCError{Method: "ProxyStreamedCall", StatusCode: 400, Message: privateErrorDetail}}, Config{})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest("POST", test.path, strings.NewReader(test.body)))
			if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), clientErrorMessage(400, "upstream_error")) {
				t.Fatalf("stream response=%d %s", recorder.Code, recorder.Body)
			}
			assertPrivateErrorAbsent(t, recorder.Body.String())
		})
	}
}

type presentationAdmin struct{ AdminService }

func (presentationAdmin) LoginAccount(context.Context, string, AccountLoginInput, *camoufoxnative.LiveView) (AdminAccount, error) {
	return AdminAccount{}, &aistudio.RPCError{StatusCode: 401, Message: privateErrorDetail}
}
func (presentationAdmin) RefreshAccount(context.Context, string) (AdminAccountRefresh, error) {
	return AdminAccountRefresh{}, &aistudio.RPCError{StatusCode: 401, Message: privateErrorDetail}
}

func TestAccountUnauthorizedDoesNotRevokeConsoleSession(t *testing.T) {
	handler := NewHandler(nil, Config{Admin: presentationAdmin{}, AdminAuthEnabled: true, AdminUsername: "synthetic", AdminPassword: "synthetic-password"})
	login := authExchange(handler, "POST", "/api/auth/login", `{"username":"synthetic","password":"synthetic-password"}`)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatal("synthetic login failed")
	}
	cookie := login.Result().Cookies()[0]
	for _, action := range []string{"refresh", "login"} {
		result := authExchange(handler, "POST", "/api/accounts/synthetic/"+action, "", cookie)
		if result.Code != 401 || !strings.Contains(result.Body.String(), `"code":"upstream_error"`) {
			t.Fatalf("operation=%s response=%d %s", action, result.Code, result.Body)
		}
		assertPrivateErrorAbsent(t, result.Body.String())
		session := authExchange(handler, "GET", "/api/auth/session", "", cookie)
		if session.Code != 200 || !strings.Contains(session.Body.String(), `"authenticated":true`) {
			t.Fatal("account authentication failure revoked the console session")
		}
	}
	unauthenticated := authExchange(handler, "POST", "/api/accounts/synthetic/refresh", "")
	if unauthenticated.Code != 401 || !strings.Contains(unauthenticated.Body.String(), `"code":"admin_login_required"`) {
		t.Fatal("missing console session no longer produces its explicit error")
	}
}

func TestAsyncAndRealtimeErrorPresentation(t *testing.T) {
	err := &aistudio.RPCError{StatusCode: 400, Message: privateErrorDetail}
	s := &server{responseStates: newResponseStateStore()}
	s.storeBackgroundEnd(context.Background(), "resp_synthetic", time.Now().Unix(), responsesRequest{Model: "public-alias"}, responsesPreparation{}, err)
	recorder := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/responses/resp_synthetic", nil)
	r.SetPathValue("response", "resp_synthetic")
	s.handleResponseGet(recorder, r)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), clientRequestErrorMessage(err)) {
		t.Fatalf("background readback=%d %s", recorder.Code, recorder.Body)
	}
	assertPrivateErrorAbsent(t, recorder.Body.String())
	rawErrorFrame, marshalErr := json.Marshal(map[string]string{"error": privateErrorDetail})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, payload := range []any{
		bidiErrorMessage(err),
		mapBidiServerEvent(aistudio.BidiEvent{Kind: aistudio.BidiEventError, Err: err, Raw: rawErrorFrame, Text: privateErrorDetail}),
		mapBidiServerEvent(aistudio.BidiEvent{Kind: aistudio.BidiEventError, Raw: rawErrorFrame, Text: privateErrorDetail}),
		openAIVideoObject(aistudio.VideoOperation{Missing: privateErrorDetail}),
	} {
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		assertPrivateErrorAbsent(t, string(body))
	}
	feedback, ok := geminiPromptFeedback(aistudio.GenerateRequest{Model: "public-alias"}, &aistudio.PromptFeedbackError{Reason: privateErrorDetail, Message: privateErrorDetail})
	if !ok {
		t.Fatal("prompt feedback was lost")
	}
	body, marshalErr := json.Marshal(feedback)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	assertPrivateErrorAbsent(t, string(body))
}

type presentationRetryError struct{}

func (presentationRetryError) Error() string                   { return privateErrorDetail }
func (presentationRetryError) HTTPStatus() int                 { return 429 }
func (presentationRetryError) RetryAt(now time.Time) time.Time { return now.Add(5 * time.Second) }

func TestErrorPresentationKeepsRetryAndContextSignals(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeGeminiRequestError(recorder, presentationRetryError{})
	if recorder.Code != 429 || recorder.Header().Get("Retry-After") == "" || !strings.Contains(recorder.Body.String(), "google.rpc.RetryInfo") {
		t.Fatalf("retry metadata lost: %d %s", recorder.Code, recorder.Body)
	}
	assertPrivateErrorAbsent(t, recorder.Body.String())
	contextErr := &aistudio.RPCError{StatusCode: 400, Message: "input token count exceeds the maximum number of tokens allowed " + privateErrorDetail}
	recorder = httptest.NewRecorder()
	writeAnthropicRequestError(recorder, contextErr)
	if !strings.Contains(recorder.Body.String(), "prompt is too long") || !strings.HasPrefix(anthropicErrorMessage(contextErr), "prompt is too long") {
		t.Fatal("context overflow signal lost")
	}
	assertPrivateErrorAbsent(t, recorder.Body.String())
}
