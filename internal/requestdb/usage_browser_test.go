package requestdb

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/webui"
)

// Opt-in local acceptance server. It never starts model workers or reads user data.
func TestUsageBrowserServer(t *testing.T) {
	out := os.Getenv("USAGE_BROWSER_OUT")
	if out == "" {
		t.Skip("set USAGE_BROWSER_OUT for isolated browser acceptance")
	}
	store, _ := usageFixture(t, 100000)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ledger := api.NewHandler(nil, api.Config{Ledger: store})
	after := webui.Handler()
	before := http.FileServer(http.Dir(filepath.Join(root, "runtime/usage-verification/frontend-before")))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var server *http.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__stop" {
			w.WriteHeader(204)
			go func() { time.Sleep(100 * time.Millisecond); server.Close() }()
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/usage") || strings.HasPrefix(r.URL.Path, "/api/requests/") {
			ledger.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			var body any = map[string]any{}
			switch r.URL.Path {
			case "/api/auth/session":
				body = map[string]any{"authenticated": true, "enabled": false}
			case "/api/status":
				body = map[string]any{"state": "STOPPED", "running": false, "ready": false, "version": "usage-fixture", "active_requests": 0, "accounts": map[string]int{"total": 0, "ready": 0, "busy": 0, "cooldown": 0, "auth_required": 0}}
			case "/api/accounts":
				body = map[string]any{"accounts": []any{}}
			case "/api/models":
				body = map[string]any{"models": []any{}}
			case "/api/cooldowns":
				body = map[string]any{"cooldowns": []any{}}
			case "/api/requests":
				body = map[string]any{"requests": []any{}}
			case "/api/logs":
				body = map[string]any{"logs": []any{}}
			case "/api/update":
				body = map[string]any{"current": "usage-fixture", "available": false, "mode": "development", "updating": false}
			}
			json.NewEncoder(w).Encode(body)
			return
		}
		if r.URL.Path == "/before/" {
			data, err := os.ReadFile(filepath.Join(root, "runtime/usage-verification/frontend-before/index.html"))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(strings.ReplaceAll(string(data), `"/assets/`, `"/before/assets/`)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/before/") {
			http.StripPrefix("/before", before).ServeHTTP(w, r)
			return
		}
		after.ServeHTTP(w, r)
	})
	server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	if err := os.WriteFile(out, []byte("http://"+listener.Addr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("acceptance server: %s", listener.Addr())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
