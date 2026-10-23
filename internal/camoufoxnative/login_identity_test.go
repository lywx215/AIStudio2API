package camoufoxnative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type loginWaitFrame struct {
	page, email string
	ready       bool
	emailError  bool
}

// Use a real BiDi transport, but keep all pages and identities synthetic.
func loginWaitFixture(t *testing.T, frames []loginWaitFrame) (*loginSession, *atomic.Int32) {
	t.Helper()
	emailReads := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		index := -1
		for {
			var message map[string]any
			if connection.ReadJSON(&message) != nil {
				return
			}
			params := message["params"].(map[string]any)
			expression, _ := params["expression"].(string)
			if expression == "location.href" && index+1 < len(frames) {
				index++
			}
			frame := frames[max(index, 0)]
			valueType, value := "string", any("[]")
			switch {
			case expression == "location.href":
				value = frame.page
			case expression == promptReadyExpression:
				valueType, value = "boolean", frame.ready
			case expression == loginEmailExpression:
				emailReads.Add(1)
				if frame.emailError {
					if connection.WriteJSON(map[string]any{"type": "error", "id": message["id"], "error": "no such frame", "message": "navigation"}) != nil {
						return
					}
					continue
				}
				value = frame.email
			case message["method"] == "script.callFunction":
				value = "aistudio_loading"
			case message["method"] != "script.evaluate":
				t.Errorf("unexpected command before verified identity: %s", message["method"])
			}
			response := map[string]any{"type": "success", "id": message["id"], "result": map[string]any{
				"type": "success", "result": map[string]any{"type": valueType, "value": value},
			}}
			if connection.WriteJSON(response) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return &loginSession{connection: connection, client: newBiDiClient(connection), contextID: "fixture"}, emailReads
}

func TestWaitLoginRequiresCompletedOnboardingAndLoadedIdentity(t *testing.T) {
	page := aiStudioOrigin + "/prompts/new_chat"
	session, reads := loginWaitFixture(t, []loginWaitFrame{
		{page: aiStudioOrigin + "/onboarding", ready: true, email: "user@example.test"},
		{page: page, ready: false, email: "user@example.test"},
		{page: page, ready: true},
		{page: page, ready: true, emailError: true},
		{page: page, ready: true, email: " User@Example.Test "},
	})
	session.automation = newLoginAutomation("user@example.test", "synthetic", NewLiveView())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gotURL, email, err := session.waitLogin(ctx, make(map[string]storageOrigin))
	if err != nil || gotURL != page || email != "user@example.test" || reads.Load() != 3 {
		t.Fatalf("identity wait: page=%q email=%q reads=%d err=%v", gotURL, email, reads.Load(), err)
	}
}

func TestWaitLoginRejectsMismatchedIdentityWithoutSaving(t *testing.T) {
	session, _ := loginWaitFixture(t, []loginWaitFrame{{page: aiStudioOrigin + "/prompts/new_chat", ready: true, email: "other@example.test"}})
	session.automation = newLoginAutomation("user@example.test", "synthetic", NewLiveView())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	page, email, err := session.waitLogin(ctx, make(map[string]storageOrigin))
	var flowError *loginFlowError
	if !errors.As(err, &flowError) || flowError.ErrorCode() != "login_identity_mismatch" || page != "" || email != "" {
		t.Fatal("mismatched identity was allowed to proceed to credential export")
	}
	if strings.Contains(err.Error(), "other@example.test") {
		t.Fatal("identity error exposed the wrong account")
	}
}

func TestWaitLoginMissingIdentityNeverFallsBackToConfiguredEmail(t *testing.T) {
	session, reads := loginWaitFixture(t, []loginWaitFrame{{page: aiStudioOrigin + "/prompts/new_chat", ready: true}})
	session.automation = newLoginAutomation("user@example.test", "synthetic", NewLiveView())
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	page, email, err := session.waitLogin(ctx, make(map[string]storageOrigin))
	if !errors.Is(err, context.DeadlineExceeded) || page != "" || email != "" || reads.Load() < 2 {
		t.Fatal("missing identity was saved, was not retried, or ignored cancellation")
	}
}

func TestWaitLoginMissingIdentityHasBoundedSpecificError(t *testing.T) {
	t.Parallel()
	session, _ := loginWaitFixture(t, []loginWaitFrame{{page: aiStudioOrigin + "/prompts/new_chat", ready: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	page, email, err := session.waitLogin(ctx, make(map[string]storageOrigin))
	var flowError *loginFlowError
	if !errors.As(err, &flowError) || flowError.ErrorCode() != "email_detection_failed" || page != "" || email != "" {
		t.Fatalf("expected a bounded email detection failure, got %v", err)
	}
}

func TestLoginPageAllowsSaveOnlyAfterOnboarding(t *testing.T) {
	for _, address := range []string{
		"http://aistudio.google.com/prompts/new_chat",
		"https://aistudio.google.com.example.test/prompts/new_chat",
		"https://example.test@aistudio.google.com/prompts/new_chat",
		"https://aistudio.google.com/onboarding",
		"https://aistudio.google.com/onboarding/?step=2",
	} {
		if loginPageAllowsSave(address) {
			t.Fatalf("allowed credential save on %s", address)
		}
	}
	if !loginPageAllowsSave(aiStudioOrigin + "/prompts/new_chat") {
		t.Fatal("completed login page was rejected")
	}
}
