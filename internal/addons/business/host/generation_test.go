package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type offlineTransport func(*http.Request) (*http.Response, error)

func (f offlineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountImportToNativeGenerationOffline(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "business", "config.json"), nil)
	defer h.Close(context.Background())
	var calls atomic.Int32
	h.transport = offlineTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"keyId":"fixture","xsrfToken":"c3ludGhldGljLWtleQ"}`
		if strings.HasSuffix(r.URL.Path, "widgetCreateSession") {
			body = `{"session":{"name":"sessions/fixture"}}`
		}
		if strings.HasSuffix(r.URL.Path, "widgetStreamAssist") {
			body = `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"fixture answer"}}}]}}}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	input := map[string]any{"revision": 0, "label": "Fixture account", "enabled": true, "credential": vault.Credential{SecureCSES: "private-fixture-cookie", CSESIndex: "index", ConfigID: "config"}}
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", input)
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private-fixture-cookie")) {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("account import contacted upstream")
	}
	c := config.Default()
	c.Enabled = true
	c.Bindings[0].Enabled = true
	c.Bindings[0].VerificationStatus = "verified"
	c.Bindings[0].Evidence = "synthetic fixture"
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("configuration change contacted upstream")
	}
	w = exchange(h.AdminHandler(), "GET", "/api/addons/business/status", nil)
	var s status
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if !s.GenerationReady || s.LiveVerified {
		t.Fatal("incorrect local/live readiness")
	}
	w = exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "hello"}}}}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture answer") || !strings.Contains(w.Body.String(), `"finishReason":"STOP"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected upstream call count", calls.Load())
	}
	c.Enabled = false
	c.Revision = 1
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", nil)
	if w.Code != 503 || calls.Load() != 3 {
		t.Fatal("disabled addon called upstream")
	}
	w = exchange(h.AdminHandler(), "GET", "/api/addons/business/accounts", nil)
	if bytes.Contains(w.Body.Bytes(), []byte("credential\"")) || bytes.Contains(w.Body.Bytes(), []byte("private-fixture-cookie")) {
		t.Fatal("credentials exposed")
	}
}

func TestDisableCancelsAnInFlightAccountLease(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "business", "config.json"), nil)
	defer h.Close(context.Background())
	entered := make(chan struct{})
	h.transport = offlineTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 0, "label": "Fixture", "enabled": true, "credential": vault.Credential{SecureCSES: "fixture", CSESIndex: "fixture", ConfigID: "fixture"}})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	c := config.Default()
	c.Enabled = true
	c.Bindings[0].Enabled = true
	c.Bindings[0].VerificationStatus = "verified"
	c.Bindings[0].Evidence = "fixture"
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	done := make(chan int, 1)
	go func() {
		done <- exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "hello"}}}}}).Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach transport")
	}
	c.Enabled = false
	c.Revision = 1
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case code := <-done:
		if code == 200 {
			t.Fatal("cancelled request became success")
		}
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel request")
	}
}
