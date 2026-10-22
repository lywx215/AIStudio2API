package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func ownedExchange(h *Host, owner, method, address, body string, headers map[string]string, upload bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, address, strings.NewReader(body))
	r = r.WithContext(contract.WithPrincipal(r.Context(), owner))
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	if upload {
		h.UploadHandler().ServeHTTP(w, r)
	} else {
		h.PublicHandler().ServeHTTP(w, r)
	}
	return w
}

func enableFixture(t *testing.T, h *Host) {
	t.Helper()
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/accounts", map[string]any{"revision": 0, "label": "Fixture", "enabled": true, "credential": vault.Credential{SecureCSES: "synthetic-cookie", CSESIndex: "index", ConfigID: "config"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := config.Default()
	c.Enabled = true
	c.MaxConcurrent = 1
	c.Bindings[0].Enabled = true
	c.Bindings[0].VerificationStatus = "verified"
	c.Bindings[0].Evidence = "synthetic offline fixture"
	w = exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHostNativeFileUploadReferenceAndRestartOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	h := New(path, nil)
	defer h.Close(context.Background())
	var calls, uploads atomic.Int32
	h.transport = offlineTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`
		if strings.HasSuffix(r.URL.Path, "widgetCreateSession") {
			body = `{"session":{"name":"sessions/current"}}`
		}
		if strings.HasSuffix(r.URL.Path, "widgetAddContextFile") {
			uploads.Add(1)
			var payload struct {
				Request struct{ Name, FileName, MIMEType, FileContents string } `json:"addContextFileRequest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(payload.Request.FileContents)
			if err != nil || string(data) != "file fixture" || payload.Request.Name != "sessions/current" || payload.Request.FileName != "fixture.txt" {
				t.Fatal("file snapshot was not materialized in current session")
			}
			body = `{"addContextFileResponse":{"fileId":"fresh-upstream-file"}}`
		}
		if strings.HasSuffix(r.URL.Path, "widgetStreamAssist") {
			var payload struct {
				Request struct {
					FileIDs []string `json:"fileIds"`
				} `json:"streamAssistRequest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Request.FileIDs) != 1 || payload.Request.FileIDs[0] != "fresh-upstream-file" {
				t.Fatal("missing current session file ID", err)
			}
			body = `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"file reply"}}}]}}}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	enableFixture(t, h)
	owner, err := h.Principal("synthetic-model-key")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := h.Principal("different-model-key")
	if err != nil {
		t.Fatal(err)
	}
	start := ownedExchange(h, owner, "POST", "http://localhost/upload/v1beta/files", `{"file":{"display_name":"fixture.txt","mime_type":"text/plain","size_bytes":"12"}}`, map[string]string{contract.ProviderHeader: "business", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "12", "X-Goog-Upload-Header-Content-Type": "text/plain"}, false)
	if start.Code != 200 {
		t.Fatal(start.Code, start.Body.String())
	}
	address := start.Header().Get("X-Goog-Upload-URL")
	final := ownedExchange(h, "", "POST", address, "file fixture", map[string]string{"X-Goog-Upload-Command": "upload, finalize", "X-Goog-Upload-Offset": "0"}, true)
	if final.Code != 200 {
		t.Fatal(final.Code, final.Body.String())
	}
	var result struct{ File struct{ Name, URI string } }
	if err := json.Unmarshal(final.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("local file upload contacted upstream")
	}
	generate := `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"text/plain","fileUri":"` + result.File.URI + `"}},{"text":"summarize"}]}]}`
	w := ownedExchange(h, owner, "POST", "http://localhost/v1beta/models/business-auto:generateContent", generate, nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "file reply") || calls.Load() != 4 || uploads.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), calls.Load())
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := New(path, nil)
	defer restarted.Close(context.Background())
	ownerAgain, err := restarted.Principal("synthetic-model-key")
	if err != nil || ownerAgain != owner {
		t.Fatal("file principal changed after restart", err)
	}
	for _, test := range []struct {
		owner string
		want  int
	}{{ownerAgain, 200}, {wrong, 404}} {
		w := ownedExchange(restarted, test.owner, "GET", "http://localhost/v1beta/"+result.File.Name, "", nil, false)
		if w.Code != test.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestConsoleNativeTrialUsesBusinessCore(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	defer h.Close(context.Background())
	h.transport = offlineTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`
		if strings.HasSuffix(r.URL.Path, "widgetCreateSession") {
			body = `{"session":{"name":"sessions/trial"}}`
		}
		if strings.HasSuffix(r.URL.Path, "widgetStreamAssist") {
			body = `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"console trial"}}}]}}}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	enableFixture(t, h)
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/try/models/business-auto:generateContent", map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "hello"}}}}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "console trial") {
		t.Fatal(w.Code, w.Body.String())
	}
}

type forbiddenBody struct{ reads atomic.Int32 }

func (b *forbiddenBody) Read([]byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (*forbiddenBody) Close() error               { return nil }

func TestHostBoundsPreparationBeforeReadingOversubscribedBodies(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	defer h.Close(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	h.transport = offlineTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return nil, context.Canceled
	})
	enableFixture(t, h)
	done := make(chan struct{})
	go func() {
		defer close(done)
		exchange(h.PublicHandler(), "POST", "/v1beta/models/business-auto:generateContent", map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "hello"}}}}})
	}()
	<-entered
	body := &forbiddenBody{}
	r := httptest.NewRequest("POST", "/v1beta/models/business-auto:generateContent", nil)
	r.Body = body
	w := httptest.NewRecorder()
	h.PublicHandler().ServeHTTP(w, r)
	close(release)
	<-done
	if w.Code != 429 || body.reads.Load() != 0 {
		t.Fatal("excess generation read body", w.Code, body.reads.Load())
	}
}
