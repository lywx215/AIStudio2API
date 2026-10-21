package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	p "github.com/Mag1cFall/AIStudio2API/internal/proxymanager"
)

func TestProxyConsolePasswordCSRFPrivateDTOAndDurableJob(t *testing.T) {
	service, err := p.Open(context.Background(), p.Options{Path: filepath.Join(t.TempDir(), "proxies.db"), Download: func(context.Context, string) ([]byte, error) {
		return []byte("proxy.invalid:1080:fixture:synthetic-proxy-password"), nil
	}, Probe: func(context.Context, string) p.ProbeResult {
		return p.ProbeResult{State: "passed", ExitIP: "8.8.8.8", LatencyMS: 4}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler := NewHandler(nil, Config{AdminAuthEnabled: true, AdminUsername: "operator", AdminPassword: "test-password", APIKey: "synthetic-model-key", ProxyAdmin: service})
	if w := authExchange(handler, "GET", "/api/proxies", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := authExchange(handler, "POST", "/api/auth/login", `{"username":"operator","password":"test-password"}`)
	cookie := login.Result().Cookies()[0]
	create := authExchange(handler, "POST", "/api/proxies/sources", `{"name":"Fixture","protocol":"socks5","url":"https://example.test/synthetic-download-token","enabled":true}`, cookie)
	if create.Code != 200 || strings.Contains(create.Body.String(), "synthetic-download-token") {
		t.Fatal(create.Code)
	}
	var source p.Source
	if json.Unmarshal(create.Body.Bytes(), &source) != nil {
		t.Fatal("source response")
	}
	refresh := authExchange(handler, "POST", "/api/proxies/sources/"+source.ID+"/refresh", "", cookie)
	if refresh.Code != 200 {
		t.Fatal(refresh.Body.String())
	}
	list := authExchange(handler, "GET", "/api/proxies?limit=50", "", cookie)
	if list.Code != 200 || strings.Contains(list.Body.String(), "synthetic-proxy-password") || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private list or cache boundary")
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/proxies/jobs", strings.NewReader(`{"kind":"test","all":true}`))
	r.Header.Set("Origin", "https://evil.invalid")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF allowed")
	}
	r = httptest.NewRequest(http.MethodPost, "http://localhost/api/proxies/jobs", strings.NewReader(`{"kind":"test","all":true}`))
	r.Header.Set("Origin", "http://localhost")
	r.Header.Set("Idempotency-Key", "synthetic-http-job")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job p.Job
	if json.Unmarshal(w.Body.Bytes(), &job) != nil {
		t.Fatal("job response")
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		read := authExchange(handler, "GET", "/api/proxies/jobs/by-key/synthetic-http-job", "", cookie)
		if read.Code != 200 || strings.Contains(read.Body.String(), "synthetic-proxy-password") {
			t.Fatal("unsafe job query")
		}
		if json.Unmarshal(read.Body.Bytes(), &job) != nil {
			t.Fatal("job decode")
		}
		if job.Status == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if job.Done != 1 || job.Items[0].Status != "succeeded" {
		t.Fatal(job)
	}
	invalid := authExchange(handler, "POST", "/api/proxies/sources", `{"name":"Fixture","url":"https://user:synthetic-secret@example.test/","enabled":true}`, cookie)
	if invalid.Code != 400 || strings.Contains(invalid.Body.String(), "synthetic-secret") {
		t.Fatal("invalid source echo")
	}
	loopback := NewHandler(nil, Config{ProxyAdmin: service})
	if w := authExchange(loopback, "GET", "/api/proxies", ""); w.Code != 403 {
		t.Fatal("loopback without password granted proxy access")
	}
}
