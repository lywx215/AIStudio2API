package host

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
)

func exchange(handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(data)))
	return w
}

func TestConfigRevisionPersistsAndConcurrentWritesConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	h := New(path, nil)
	c := config.Default()
	c.Enabled = true
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c); w.Code != 409 {
		t.Fatal(w.Code)
	}
	got, err := config.Load(path, nil)
	if err != nil || got.Revision != 1 || !got.Enabled {
		t.Fatalf("revision=%d err=%v", got.Revision, err)
	}
	restarted := New(path, nil)
	w := exchange(restarted.AdminHandler(), "GET", "/api/addons/business/status", nil)
	var s status
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if s.GenerationReady || s.State != "unavailable" || s.Code != "BUSINESS_ACCOUNT_REQUIRED" {
		t.Fatal("reported false readiness")
	}
}

func TestConcurrentConfigWritesHaveExactlyOneWinner(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	start := make(chan struct{})
	results := make(chan int, 12)
	for range 12 {
		go func() {
			<-start
			results <- exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", config.Default()).Code
		}()
	}
	close(start)
	winners := 0
	for range 12 {
		switch code := <-results; code {
		case 200:
			winners++
		case 409:
		default:
			t.Fatalf("unexpected save result: %d", code)
		}
	}
	if winners != 1 {
		t.Fatalf("successful writes=%d", winners)
	}
}

func TestInvalidConfigIsContainedAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"credential":"synthetic-secret","broken":`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	h := New(path, nil)
	w := exchange(h.AdminHandler(), "GET", "/api/addons/business/status", nil)
	if bytes.Contains(w.Body.Bytes(), []byte("synthetic-secret")) || !bytes.Contains(w.Body.Bytes(), []byte("BUSINESS_CONFIG_INVALID")) {
		t.Fatal(w.Body.String())
	}
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", config.Default()); w.Code != 409 {
		t.Fatal(w.Code)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatal("damaged settings overwritten")
	}
}

func TestDisabledRequestsAndForgedUploadsTerminateWithoutBodyReads(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "business", "config.json"), nil)
	for _, path := range []string{"/v1beta/models/business-auto:generateContent", "/v1beta/models", "/upload/v1beta/files"} {
		w := exchange(h.PublicHandler(), "POST", path, map[string]any{"contents": "must not be forwarded"})
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
		var envelope map[string]any
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope["error"] == nil {
			t.Fatal("not a native error envelope")
		}
	}
	if w := exchange(h.UploadHandler(), "POST", "/upload/v1beta/files?upload_id=biz-forged", nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", config.Default()); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestAliasConflictDoesNotCreateStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	h := New(path, func(id string) bool { return id == "business-auto" })
	c := config.Default()
	b := &c.Bindings[0]
	b.Enabled = true
	b.VerificationStatus = "verified"
	b.Evidence = "synthetic fixture"
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid settings persisted")
	}
}
