package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	addonconfig "github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	addonhost "github.com/Mag1cFall/AIStudio2API/internal/addons/business/host"
	"github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestAddonNativeFilesUseDurableAuthenticatedOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	c := addonconfig.Default()
	c.Enabled = true
	if err := addonconfig.Save(path, c); err != nil {
		t.Fatal(err)
	}
	newAttached := func() (*addonhost.Host, http.Handler) {
		host := addonhost.New(path, nil)
		cfg := Config{APIKey: "first,second", Business: &BusinessAddon{Public: host.PublicHandler(), Admin: host.AdminHandler(), Upload: host.UploadHandler(), Principal: host.Principal}}
		return host, attachBusiness(http.NotFoundHandler(), cfg, newAdminAuth(cfg))
	}
	host, h := newAttached()
	defer host.Close(context.Background())
	call := func(handler http.Handler, method, address, body, key string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, address, strings.NewReader(body))
		if key != "" {
			r.Header.Set("X-Goog-API-Key", key)
		}
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	start := call(h, "POST", "http://localhost/upload/v1beta/files", `{"file":{"display_name":"scope.txt","mime_type":"text/plain"}}`, "first", map[string]string{contract.ProviderHeader: "business", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "7", "X-Goog-Upload-Header-Content-Type": "text/plain"})
	if start.Code != 200 {
		t.Fatal(start.Code, start.Body.String())
	}
	// SDK upload capabilities remain usable when later chunks omit API headers.
	final := call(h, "POST", start.Header().Get("X-Goog-Upload-URL"), "fixture", "", map[string]string{"X-Goog-Upload-Command": "upload, finalize", "X-Goog-Upload-Offset": "0"})
	if final.Code != 200 {
		t.Fatal(final.Code, final.Body.String())
	}
	var result struct{ File struct{ Name string } }
	if err := json.Unmarshal(final.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, rebound := newAttached()
	defer restarted.Close(context.Background())
	for _, test := range []struct {
		key  string
		want int
	}{{"", 401}, {"second", 404}, {"first", 200}} {
		w := call(rebound, "GET", "http://localhost/v1beta/"+result.File.Name, "", test.key, nil)
		if w.Code != test.want {
			t.Fatal("model API key file isolation failed", test.key, w.Code, w.Body.String())
		}
	}
	for _, key := range []string{"first", "second"} {
		w := call(rebound, "POST", "http://localhost/api/addons/business/try/models/business-auto:generateContent", `{}`, key, nil)
		// Console auth is disabled here, so non-loopback test clients remain forbidden.
		if w.Code != 403 {
			t.Fatal("model key bypassed console origin boundary", w.Code)
		}
	}
}

type unreadAddonBody struct{ reads int }

func (b *unreadAddonBody) Read([]byte) (int, error) { b.reads++; return 0, errors.New("must not read") }
func (*unreadAddonBody) Close() error               { return nil }

func TestAddonPassThroughPreservesRequestAndWriter(t *testing.T) {
	for _, path := range []string{
		"/v1/chat/completions", "/v1beta/models/legacy:generateContent", "/upload/v1beta/files",
		"/upload/v1beta/files?upload_id=legacy-id", "/v1beta/files/abc123", "/api/status", "/health",
		managementcontract.Namespace + "/accounts", managementcontract.KeyNamespace,
	} {
		t.Run(path, func(t *testing.T) {
			body := &unreadAddonBody{}
			r := httptest.NewRequest("POST", "http://localhost"+path, nil)
			r.Body = body
			w := httptest.NewRecorder()
			calls := 0
			cfg := Config{Business: &BusinessAddon{}}
			h := attachBusiness(http.HandlerFunc(func(gotW http.ResponseWriter, gotR *http.Request) {
				calls++
				if gotW != w || gotR != r || gotR.Body != body {
					t.Fatal("legacy request was wrapped")
				}
				gotW.WriteHeader(202)
			}), cfg, newAdminAuth(cfg))
			h.ServeHTTP(w, r)
			if calls != 1 || body.reads != 0 || w.Code != 202 {
				t.Fatalf("calls=%d reads=%d code=%d", calls, body.reads, w.Code)
			}
		})
	}
}

func TestAddonBridgeSharedModelAuthentication(t *testing.T) {
	var principals []string
	cfg := Config{APIKey: "first, second", Business: &BusinessAddon{Public: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principals = append(principals, contract.Principal(r.Context()))
		w.WriteHeader(204)
	})}}
	h := NewHandler(nil, cfg)
	for _, test := range []struct {
		path, header, value string
		status              int
	}{
		{"/v1beta/models/business-demo:generateContent", "", "", 401},
		{"/v1beta/models/business-demo:generateContent", "X-Goog-API-Key", "first", 204},
		{"/v1beta/models/business-demo:generateContent?key=first", "", "", 204},
		{"/v1beta/models/business-demo:generateContent", "Authorization", "Bearer second", 204},
	} {
		r := httptest.NewRequest("POST", "http://localhost"+test.path, nil)
		if test.header != "" {
			r.Header.Set(test.header, test.value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("code=%d want=%d", w.Code, test.status)
		}
	}
	if len(principals) != 3 || principals[0] != principals[1] || principals[0] == principals[2] || strings.Contains(principals[0], "first") {
		t.Fatalf("principal separation failed: %d", len(principals))
	}
	for _, key := range []string{"", "first"} {
		r := httptest.NewRequest("POST", "/v1beta/models/unknown:generateContent", nil)
		r.Header.Set(contract.ProviderHeader, "business")
		r.Header.Set("X-Goog-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if key == "" {
			want = 401
		}
		if w.Code != want {
			t.Fatalf("invalid routing must authenticate first: %d", w.Code)
		}
	}
}

func TestAddonBridgeSharesConsoleSessionAndCancellation(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	cfg := Config{AdminAuthEnabled: true, AdminUsername: "operator", AdminPassword: "test-password", APIKey: "model-key",
		Business: &BusinessAddon{Admin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/wait") {
				close(entered)
				<-r.Context().Done()
				close(cancelled)
			}
			w.WriteHeader(204)
		})}}
	h := NewHandler(nil, cfg)
	if got := authExchange(h, "GET", "/api/addons/business/status", ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	r := httptest.NewRequest("GET", "http://localhost/api/addons/business/status", nil)
	r.Header.Set("Authorization", "Bearer model-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("model key accepted as console session")
	}
	login := authExchange(h, "POST", "/api/auth/login", `{"username":"operator","password":"test-password"}`)
	if login.Code != 200 || len(login.Result().Cookies()) == 0 {
		t.Fatal("login failed")
	}
	cookie := login.Result().Cookies()[0]
	if got := authExchange(h, "GET", "/api/addons/business/status", "", cookie); got.Code != 204 {
		t.Fatal(got.Code)
	}
	r = httptest.NewRequest("GET", "http://localhost/api/addons/business/status", nil)
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin request accepted")
	}
	go authExchange(h, "GET", "/api/addons/business/wait", "", cookie)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not reached")
	}
	authExchange(h, "POST", "/api/auth/logout", "", cookie)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("logout did not cancel Business request")
	}
	if got := authExchange(h, "GET", "/api/addons/business/status", "", cookie); got.Code != 401 {
		t.Fatal(got.Code)
	}
}

func TestAddonBridgeDisabledConsoleRemainsLoopbackOnly(t *testing.T) {
	h := NewHandler(nil, Config{Business: &BusinessAddon{Admin: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })}})
	for _, address := range []string{"127.0.0.1:2000", "192.0.2.1:2000"} {
		r := httptest.NewRequest("GET", "http://localhost/api/addons/business/status", nil)
		r.RemoteAddr = address
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 403
		if strings.HasPrefix(address, "127.") {
			want = 204
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
}

func TestAddonUploadPrecedesNativeUploadInterceptor(t *testing.T) {
	legacyCalls, uploadCalls := 0, 0
	cfg := Config{APIKey: "test-key", Business: &BusinessAddon{Upload: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploadCalls++
		contract.Error(w, 404, "NOT_FOUND", "Unknown upload capability")
	})}}
	h := attachBusiness(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { legacyCalls++; w.WriteHeader(202) }), cfg, newAdminAuth(cfg))
	for _, test := range []struct {
		id   string
		want int
	}{{"biz-forged", 404}, {"old-capability", 202}} {
		r := httptest.NewRequest("POST", "/upload/v1beta/files?upload_id="+test.id, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatal(w.Code)
		}
	}
	if legacyCalls != 1 || uploadCalls != 1 {
		t.Fatalf("native=%d upload=%d", legacyCalls, uploadCalls)
	}
}

func TestAddonBridgeLegacyAliasesAndManagementHavePriority(t *testing.T) {
	cfg := Config{Business: &BusinessAddon{OwnsLegacy: func(id string) bool { return id == "business-existing" }}}
	h := attachBusiness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "native") }), cfg, newAdminAuth(cfg))
	for _, path := range []string{"/v1beta/models/business-existing:generateContent", managementcontract.Namespace + "/accounts", managementcontract.KeyNamespace, "/api/status"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set(contract.ProviderHeader, "business")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Body.String() != "native" {
			t.Fatalf("intercepted %s", path)
		}
	}
}
