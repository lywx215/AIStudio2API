package camoufoxnative

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func automationFixture(t *testing.T, phases []string) (*loginSession, chan string) {
	return automationFixtureWithOnboarding(t, phases, nil)
}

func automationFixtureWithOnboarding(t *testing.T, phases, onboarding []string) (*loginSession, chan string) {
	t.Helper()
	actions := make(chan string, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		index, onboardingIndex := 0, 0
		for {
			var message map[string]any
			if connection.ReadJSON(&message) != nil {
				return
			}
			params := message["params"].(map[string]any)
			arguments := params["arguments"].([]any)
			action := arguments[0].(map[string]any)["value"].(string)
			phase := "submitted"
			if params["functionDeclaration"] == loginOnboardingFunction {
				phase = "not_onboarding"
				if action == "inspect" && onboardingIndex < len(onboarding) {
					phase = onboarding[onboardingIndex]
					onboardingIndex++
				} else if action != "inspect" {
					if arguments[1].(map[string]any)["value"] != true && action == "onboarding_terms" {
						phase = "consent_required"
					} else {
						actions <- action
						phase = "submitted"
					}
				}
			} else if action == "inspect" {
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

func TestLoginAutomationOnboardingSubmitsEachStageOnce(t *testing.T) {
	session, actions := automationFixtureWithOnboarding(t, nil, []string{
		"onboarding_terms", "onboarding_terms", "onboarding_preferences", "onboarding_preferences", "aistudio_loading",
	})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "synthetic", view)
	automation.acceptTerms = true
	for range 5 {
		automation.step(context.Background(), session)
	}
	for _, expected := range []string{"onboarding_terms", "onboarding_preferences"} {
		select {
		case actual := <-actions:
			if actual != expected {
				t.Fatalf("expected %s, got %s", expected, actual)
			}
		default:
			t.Fatalf("missing %s action", expected)
		}
	}
	if len(actions) != 0 || automation.err != nil || view.status != "verifying_identity" || automation.blocked {
		t.Fatal("onboarding repeated an action or fell back to unknown-page blocking")
	}
}

func TestLoginAutomationOnboardingWaitsForConsentAndRespectsTakeover(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		session, actions := automationFixtureWithOnboarding(t, nil, []string{"consent_required"})
		view := NewLiveView()
		automation := newLoginAutomation("user@example.test", "synthetic", view)
		if takeover {
			automation.acceptTerms = true
			view.takeOver()
		}
		automation.step(context.Background(), session)
		if len(actions) != 0 || automation.err != nil {
			t.Fatal("waiting for consent or manual takeover must not submit anything")
		}
		if !takeover && view.status != "consent_required" {
			t.Fatal("missing per-login consent was not presented")
		}
	}
}

func TestLoginAutomationOnboardingTimeoutAndIdentityBlock(t *testing.T) {
	session, actions := automationFixtureWithOnboarding(t, nil, []string{"onboarding_loading"})
	automation := newLoginAutomation("user@example.test", "synthetic", NewLiveView())
	automation.acceptTerms = true
	automation.onboardingSince = time.Now().Add(-time.Minute - time.Second)
	automation.step(context.Background(), session)
	if err, ok := automation.err.(*loginFlowError); !ok || err.ErrorCode() != "onboarding_timeout" || len(actions) != 0 {
		t.Fatal("stalled onboarding did not stop with a bounded error")
	}
	session, actions = automationFixtureWithOnboarding(t, nil, []string{"onboarding_terms"})
	automation = newLoginAutomation("user@example.test", "synthetic", NewLiveView())
	automation.acceptTerms, automation.blocked = true, true
	automation.step(context.Background(), session)
	if len(actions) != 0 || automation.view.status != "manual" {
		t.Fatal("a blocked known account must not accept terms")
	}
}

func TestLoginAutomationNewAccountNeverTypesSentinelButCanCompleteAuthorizedOnboarding(t *testing.T) {
	session, actions := automationFixtureWithOnboarding(t, []string{"email"}, []string{"not_onboarding", "onboarding_terms"})
	automation := newLoginAutomation("new", "", NewLiveView())
	automation.acceptTerms = true
	automation.step(context.Background(), session)
	if len(actions) != 0 || automation.email != "" {
		t.Fatal("the new-account sentinel was used as a Google identity")
	}
	automation.step(context.Background(), session)
	if len(actions) != 1 || <-actions != "onboarding_terms" {
		t.Fatal("a manually authenticated new account could not complete authorized onboarding")
	}
}
