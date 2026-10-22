package camoufoxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func automationFixture(t *testing.T, phases []string) (*loginSession, chan string) {
	t.Helper()
	actions := make(chan string, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		index := 0
		for {
			var message map[string]any
			if connection.ReadJSON(&message) != nil {
				return
			}
			params := message["params"].(map[string]any)
			arguments := params["arguments"].([]any)
			action := arguments[0].(map[string]any)["value"].(string)
			phase := "submitted"
			if action == "inspect" {
				if index < len(phases) {
					phase = phases[index]
					index++
				} else {
					phase = "unknown"
				}
			} else if action == "diagnose" {
				phase = "diag_unknown"
			} else {
				actions <- action
			}
			if connection.WriteJSON(map[string]any{"type": "success", "id": message["id"], "result": map[string]any{"type": "success", "result": map[string]any{"type": "string", "value": phase}}}) != nil {
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
	return &loginSession{connection: connection, client: newBiDiClient(connection), contextID: "fixture"}, actions
}

func TestLoginAutomationSubmitsOnceAndHandlesPostChallengeReminder(t *testing.T) {
	session, actions := automationFixture(t, []string{"email", "email", "password", "password", "manual", "password", "later", "later"})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "synthetic", view)
	for range 8 {
		automation.step(context.Background(), session)
	}
	for _, expected := range []string{"email", "password", "later"} {
		select {
		case actual := <-actions:
			if actual != expected {
				t.Fatal("unexpected automatic action")
			}
		default:
			t.Fatal("missing automatic action")
		}
	}
	if len(actions) != 0 {
		t.Fatal("automatic action repeated")
	}
	if !automation.blocked {
		t.Fatal("challenge did not stop credential entry")
	}
}

func TestLoginAutomationExplicitTakeoverStopsAllActions(t *testing.T) {
	session, actions := automationFixture(t, []string{"password", "later"})
	view := NewLiveView()
	view.takeOver()
	automation := newLoginAutomation("user@example.test", "synthetic", view)
	automation.step(context.Background(), session)
	if len(actions) != 0 || view.status != "manual" {
		t.Fatal("manual takeover did not stop automation")
	}
}

func TestLoginAutomationMissingPasswordRetainsManualLogin(t *testing.T) {
	session, actions := automationFixture(t, []string{"email", "password"})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "", view)
	automation.step(context.Background(), session)
	automation.step(context.Background(), session)
	if len(actions) != 1 || <-actions != "email" || view.status != "manual" {
		t.Fatal("missing password did not preserve manual path")
	}
}
