package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func hotUpdateHost(t *testing.T, transport http.RoundTripper) (*Host, vault.Account) {
	t.Helper()
	h := New(filepath.Join(t.TempDir(), "business", "config.json"), nil)
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	h.transport = transport
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 0, "label": "Fixture", "enabled": true, "credential": vault.Credential{SecureCSES: "synthetic", CSESIndex: "synthetic", ConfigID: "synthetic"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := config.Default()
	c.Enabled = true
	c.Bindings[0].Enabled, c.Bindings[0].VerificationStatus, c.Bindings[0].Evidence = true, "verified", "synthetic"
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, err := h.accounts.Load()
	if err != nil {
		t.Fatal(err)
	}
	return h, s.Accounts[0]
}

func fixtureResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func nativeFixtureBody() any {
	return map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "hello"}}}}}
}

func currentConfig(t *testing.T, h *Host) config.Config {
	t.Helper()
	w := exchange(h.AdminHandler(), "GET", "/api/addons/business/config", nil)
	var c config.Config
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatal("cannot read config")
	}
	return c
}

func TestMetadataAndRoutingSaveKeepInflightSnapshotAndToken(t *testing.T) {
	entered, proceed := make(chan struct{}), make(chan struct{})
	defer close(proceed)
	var authCalls, streamCalls atomic.Int32
	models := make(chan string, 2)
	h, a := hotUpdateHost(t, offlineTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "getoxsrf"):
			authCalls.Add(1)
			return fixtureResponse(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
		case strings.HasSuffix(r.URL.Path, "widgetCreateSession"):
			return fixtureResponse(200, `{"session":{"name":"sessions/fixture"}}`), nil
		default:
			var input struct {
				Request struct {
					Generation struct {
						Model string `json:"modelId"`
					} `json:"assistGenerationConfig"`
				} `json:"streamAssistRequest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				return nil, err
			}
			models <- input.Request.Generation.Model
			if streamCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-proceed:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return fixtureResponse(200, `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"fixture complete"}}}]}}}]`), nil
		}
	}))
	engine := h.engine
	done := make(chan int, 1)
	go func() {
		done <- exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", nativeFixtureBody()).Code
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("not entered")
	}
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 1, "id": a.ID, "label": "renamed", "enabled": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, err := h.accounts.Load()
	if err != nil || s.Revision != 2 || s.Accounts[0].Epoch != a.Epoch {
		t.Fatal("metadata changed credential epoch")
	}
	c := currentConfig(t, h)
	c.Bindings[0].DisplayName = "new label"
	c.Bindings[0].SelectorMode, c.Bindings[0].UpstreamModelID = "exact", "new-model"
	c.MaxConcurrent, c.RequestTimeoutSeconds = 3, 240
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 || h.engine != engine {
		t.Fatal("config replaced stable engine", w.Code)
	}
	select {
	case <-done:
		t.Fatal("save interrupted in-flight request")
	default:
	}
	proceed <- struct{}{}
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not finish")
	}
	w = exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", nativeFixtureBody())
	if w.Code != 200 || authCalls.Load() != 1 {
		t.Fatal("JWT was lost after metadata save", w.Code, authCalls.Load())
	}
	if <-models != "" || <-models != "new-model" {
		t.Fatal("routing snapshots were mixed")
	}
}

func TestAccountErrorsSurviveConfigurationAndLabelSave(t *testing.T) {
	for _, tc := range []struct {
		code  int
		state string
	}{{429, "cooldown"}, {401, "auth_required"}} {
		t.Run(tc.state, func(t *testing.T) {
			var calls atomic.Int32
			h, a := hotUpdateHost(t, offlineTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return fixtureResponse(tc.code, `{}`), nil
			}))
			_ = exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", nativeFixtureBody())
			c := currentConfig(t, h)
			c.Bindings[0].DisplayName = "new model label"
			c.MaxConcurrent = 4
			if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c); w.Code != 200 {
				t.Fatal(w.Code)
			}
			if w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 1, "id": a.ID, "label": "new account label", "enabled": true}); w.Code != 200 {
				t.Fatal(w.Code)
			}
			states := h.engine.States()
			if len(states) != 1 || states[0].State != tc.state {
				t.Fatal("runtime failure state lost", states)
			}
			_ = exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", nativeFixtureBody())
			if calls.Load() != 1 {
				t.Fatal("cooldown/auth_required was bypassed")
			}
			if w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 2, "id": a.ID, "label": "replaced", "enabled": true, "credential": a.Credential}); w.Code != 200 {
				t.Fatal(w.Code)
			}
			s, _ := h.accounts.Load()
			if s.Accounts[0].Epoch != a.Epoch+1 || h.engine.States()[0].State != "ready" {
				t.Fatal("explicit credential replacement did not reset version")
			}
		})
	}
}

type gatedRequestBody struct {
	entered, proceed chan struct{}
	reader           io.Reader
	started          bool
}

func (r *gatedRequestBody) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		close(r.entered)
		<-r.proceed
	}
	return r.reader.Read(p)
}

func TestDisableReenableDoesNotReviveARequestStillParsing(t *testing.T) {
	var calls atomic.Int32
	h, _ := hotUpdateHost(t, offlineTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureResponse(500, `{}`), nil
	}))
	body, _ := json.Marshal(nativeFixtureBody())
	gate := &gatedRequestBody{entered: make(chan struct{}), proceed: make(chan struct{}), reader: strings.NewReader(string(body))}
	defer close(gate.proceed)
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.PublicHandler().ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-auto:generateContent", gate))
		done <- w.Code
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("body parsing did not start")
	}
	engine := h.engine
	for _, enabled := range []bool{false, true} {
		c := currentConfig(t, h)
		c.Enabled = enabled
		if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if h.engine != engine {
		t.Fatal("off/on replaced scheduler and lost draining leases")
	}
	gate.proceed <- struct{}{}
	select {
	case code := <-done:
		if code == 200 || calls.Load() != 0 {
			t.Fatal("old activation request reached new credentials", code, calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled parsing request did not finish")
	}
}
