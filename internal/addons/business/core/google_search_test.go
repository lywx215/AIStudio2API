package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func googleSearchConfig(provider string) string {
	raw, _ := json.Marshal(map[string]any{"anonymousWidgetConfig": map[string]any{
		"assistantSettings": map[string]string{"webGroundingType": provider},
	}})
	return string(raw)
}

func googleSearchRequest() contract.Request {
	input := request()
	input.GoogleSearch = true
	return input
}

func TestGoogleSearchLookupAndExactPayload(t *testing.T) {
	var actions []string
	jwt := ""
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		actions = append(actions, action)
		if action == "getoxsrf" {
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		}
		if r.URL.Scheme != "https" || r.URL.Host != "biz-discoveryengine.googleapis.com" || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Method != http.MethodPost {
			t.Fatal("lookup or generation escaped its fixed authenticated origin")
		}
		if r.Header.Get("Origin") != "https://business.gemini.google" || r.Header.Get("Referer") != "https://business.gemini.google/" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("missing Business transport headers")
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) <= len("Bearer ") {
			t.Fatal("missing lookup JWT")
		}
		if jwt == "" {
			jwt = authorization
		} else if jwt != authorization {
			t.Fatal("lookup switched its lease JWT")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid request JSON")
		}
		switch action {
		case "lookupWidgetConfig":
			if len(body) != 2 || string(body["widgetConfigId"]) != `"synthetic-config"` || string(body["modelInfoView"]) != `"END_USER_WEB"` {
				t.Fatal("lookup request was not the exact minimal contract")
			}
			return reply(200, googleSearchConfig(googleSearchProvider)), nil
		case "widgetCreateSession":
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		case "widgetStreamAssist":
			var assist map[string]json.RawMessage
			if json.Unmarshal(body["streamAssistRequest"], &assist) != nil {
				t.Fatal("missing assist request")
			}
			if string(assist["toolsSpec"]) != `{"webGroundingSpec":{}}` || len(assist) != 6 {
				t.Fatal("unexpected implicit tool or assist request field")
			}
			return reply(200, stream), nil
		default:
			t.Fatalf("unexpected action %s", action)
			return nil, errors.New("unexpected synthetic action")
		}
	})
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, transport)
	defer e.Close()
	for range 2 {
		if err := e.Generate(context.Background(), errorSelection(), googleSearchRequest(), discardEvent); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"getoxsrf", "lookupWidgetConfig", "widgetCreateSession", "widgetStreamAssist", "lookupWidgetConfig", "widgetCreateSession", "widgetStreamAssist"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("lookup was cached, reordered, or retried: %v", actions)
	}
}

func TestGoogleSearchDefaultDoesNotLookupOrEnableTools(t *testing.T) {
	var actions []string
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		actions = append(actions, action)
		switch action {
		case "getoxsrf":
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		case "widgetCreateSession":
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		case "widgetStreamAssist":
			var body struct {
				Assist map[string]json.RawMessage `json:"streamAssistRequest"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid request")
			}
			if _, ok := body.Assist["toolsSpec"]; ok {
				t.Fatal("ordinary request implicitly enabled tools")
			}
			return reply(200, stream), nil
		default:
			t.Fatalf("ordinary request performed %s", action)
			return nil, errors.New("unexpected synthetic action")
		}
	}))
	defer e.Close()
	if err := e.Generate(context.Background(), errorSelection(), request(), discardEvent); err != nil || len(actions) != 3 {
		t.Fatal(actions, err)
	}
}

func TestGoogleSearchProviderValidationBeforeSession(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"enterprise", googleSearchConfig("WEB_GROUNDING_TYPE_ENTERPRISE_WEB_SEARCH"), 400},
		{"disabled", googleSearchConfig("WEB_GROUNDING_TYPE_DISABLED"), 400},
		{"unspecified", googleSearchConfig("WEB_GROUNDING_TYPE_UNSPECIFIED"), 400},
		{"empty-enum", googleSearchConfig(""), 400},
		{"unknown-enum", googleSearchConfig("synthetic-secret"), 400},
		{"wrong-enum-case", googleSearchConfig("web_grounding_type_google_search"), 400},
		{"enum-space", googleSearchConfig(googleSearchProvider + " "), 400},
		{"missing", `{}`, 400},
		{"null-config", `{"anonymousWidgetConfig":null}`, 400},
		{"missing-provider", `{"anonymousWidgetConfig":{"assistantSettings":{}}}`, 400},
		{"legacy-boolean-only", `{"anonymousWidgetConfig":{"assistantSettings":{"googleSearchGroundingEnabled":true}}}`, 400},
		{"wrong-key-case", `{"AnonymousWidgetConfig":{"assistantSettings":{"webGroundingType":"WEB_GROUNDING_TYPE_GOOGLE_SEARCH"}}}`, 400},
		{"null-provider", `{"anonymousWidgetConfig":{"assistantSettings":{"webGroundingType":null}}}`, 400},
		{"truncated", `{`, 502},
		{"trailing", googleSearchConfig(googleSearchProvider) + `{}`, 502},
		{"array-root", `[]`, 502},
		{"null-root", `null`, 502},
		{"array-settings", `{"anonymousWidgetConfig":{"assistantSettings":[]}}`, 502},
		{"numeric-provider", `{"anonymousWidgetConfig":{"assistantSettings":{"webGroundingType":7}}}`, 502},
		{"duplicate-root", `{"anonymousWidgetConfig":{},"anonymousWidgetConfig":{"assistantSettings":{"webGroundingType":"WEB_GROUNDING_TYPE_GOOGLE_SEARCH"}}}`, 502},
		{"duplicate-provider", `{"anonymousWidgetConfig":{"assistantSettings":{"webGroundingType":"WEB_GROUNDING_TYPE_DISABLED","webGroundingType":"WEB_GROUNDING_TYPE_GOOGLE_SEARCH"}}}`, 502},
		{"invalid-utf8", strings.Replace(googleSearchConfig(googleSearchProvider), "}", ",\"extra\":\""+string([]byte{0xff})+"\"}", 1), 502},
		{"invalid-surrogate", `{"anonymousWidgetConfig":{"assistantSettings":{"webGroundingType":"WEB_GROUNDING_TYPE_GOOGLE_SEARCH"}},"extra":"\ud800"}`, 502},
		{"over-budget", googleSearchConfig(googleSearchProvider) + strings.Repeat(" ", maxWidgetConfigBytes), 502},
		{"oversized-error-only", `{"error":{"code":400,"message":"` + strings.Repeat("x", maxErrorBody) + `"}}`, 502},
		{"oversized-error-with-provider", `{"error":{"code":400,"message":"` + strings.Repeat("x", maxErrorBody) + `"},` + googleSearchConfig(googleSearchProvider)[1:], 502},
		{"short-unknown-error-with-provider", `{"error":{"message":"synthetic-secret"},` + googleSearchConfig(googleSearchProvider)[1:], 502},
		{"short-empty-error-with-provider", `{"error":{},` + googleSearchConfig(googleSearchProvider)[1:], 502},
		{"short-scalar-error-with-provider", `{"error":false,` + googleSearchConfig(googleSearchProvider)[1:], 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups, other, emitted := 0, 0, 0
			e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/getoxsrf") {
					return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/lookupWidgetConfig") {
					lookups++
					return reply(200, tc.body), nil
				}
				other++
				return reply(200, stream), nil
			}))
			defer e.Close()
			err := e.Generate(context.Background(), errorSelection(), googleSearchRequest(), func(contract.Event) error { emitted++; return nil })
			kind := ErrorProtocol
			if tc.code == 400 {
				kind = ErrorLocalParameter
			}
			operation := requireOperation(t, err, kind, StageConfigLookup, tc.code)
			if lookups != 1 || other != 0 || emitted != 0 || operation.Submitted || operation.OutputStarted || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("lookup failure generated, retried, or leaked data", lookups, other, emitted, err)
			}
			if tc.code == 400 && e.States()[0].State != "ready" {
				t.Fatal("unsupported provider was treated as account failure", e.States()[0].State)
			}
		})
	}
}

func TestGoogleSearchLookupHTTPClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
		code   int
	}{
		{"unauthenticated", 401, `{}`, ErrorAuthentication, 503},
		{"permission", 403, `{}`, ErrorPermissionDenied, 403},
		{"quota", 429, `{}`, ErrorQuotaExhausted, 429},
		{"transient", 503, `{}`, ErrorTransient, 502},
		{"bad-request", 400, `{}`, ErrorUpstreamRejected, 400},
		{"rpc-auth", 200, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"synthetic-secret"}}`, ErrorAuthentication, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups, other := 0, 0
			e, _ := classifiedEngine(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/getoxsrf") {
					return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/lookupWidgetConfig") {
					lookups++
					return reply(tc.status, tc.body), nil
				}
				other++
				return reply(200, stream), nil
			}))
			operation := requireOperation(t, e.Generate(context.Background(), errorSelection(), googleSearchRequest(), discardEvent), tc.kind, StageConfigLookup, tc.code)
			if lookups != 1 || other != 0 || operation.Submitted || strings.Contains(operation.Error(), "synthetic-secret") {
				t.Fatal("lookup HTTP failure generated or retried", lookups, other, operation)
			}
		})
	}
}

func TestGoogleSearchLookupCancellation(t *testing.T) {
	entered := make(chan struct{})
	other := 0
	e := New(context.Background(), []vault.Account{account()}, 1, time.Minute, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getoxsrf") {
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/lookupWidgetConfig") {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		other++
		return reply(200, stream), nil
	}))
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Generate(ctx, errorSelection(), googleSearchRequest(), discardEvent) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		operation := requireOperation(t, err, ErrorClientCancelled, StageConfigLookup, 499)
		if !errors.Is(err, context.Canceled) || operation.Submitted || other != 0 || e.States()[0].State != "ready" {
			t.Fatal("cancelled lookup submitted a generation or penalized account", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled lookup did not finish")
	}
}

type googleSearchCountingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *googleSearchCountingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *googleSearchCountingBody) Close() error { b.closed = true; return nil }

func TestGoogleSearchLookupBodyBudgetAndClose(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(string(rune('0'+extra)), func(t *testing.T) {
			config := googleSearchConfig(googleSearchProvider)
			body := &googleSearchCountingBody{Reader: strings.NewReader(config + strings.Repeat(" ", maxWidgetConfigBytes-len(config)+extra))}
			e := New(context.Background(), nil, 1, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			}))
			defer e.Close()
			err := e.requireGoogleSearch(context.Background(), &lease{account: account(), token: "synthetic"})
			if (err != nil) != (extra != 0) || !body.closed || body.read != maxWidgetConfigBytes+extra {
				t.Fatal("lookup read exceeded its limit, leaked body, or rejected exact limit", body.read, body.closed, err)
			}
		})
	}
}

func TestGoogleSearchRechecksChangedProvider(t *testing.T) {
	lookups, sessions, generations := 0, 0, 0
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getoxsrf"):
			return reply(200, `{"keyId":"synthetic","xsrfToken":"c3ludGhldGlj"}`), nil
		case strings.HasSuffix(r.URL.Path, "/lookupWidgetConfig"):
			lookups++
			provider := googleSearchProvider
			if lookups > 1 {
				provider = "WEB_GROUNDING_TYPE_ENTERPRISE_WEB_SEARCH"
			}
			return reply(200, googleSearchConfig(provider)), nil
		case strings.HasSuffix(r.URL.Path, "/widgetCreateSession"):
			sessions++
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		default:
			generations++
			return reply(200, stream), nil
		}
	}))
	defer e.Close()
	if err := e.Generate(context.Background(), errorSelection(), googleSearchRequest(), discardEvent); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, e.Generate(context.Background(), errorSelection(), googleSearchRequest(), discardEvent), ErrorLocalParameter, StageConfigLookup, 400)
	if lookups != 2 || sessions != 1 || generations != 1 {
		t.Fatal("stale provider reused after a configuration change", lookups, sessions, generations)
	}
}
