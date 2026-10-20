package api

import (
	"net/http"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// registrationProbe checks only G0 dispatch and console provenance. It is not a
// substitute for G3's real HTTP or task-service acceptance.
type registrationProbe struct{ seenPath string }

func (p *registrationProbe) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.seenPath = r.URL.Path; w.WriteHeader(204) })
}
func (p *registrationProbe) KeyHandler(auth managementcontract.ConsoleAuthorizer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := auth.AuthorizeConsole(r); err != nil {
			w.WriteHeader(managementcontract.PublicError(err).HTTPStatus())
			return
		}
		p.seenPath = r.URL.Path
		w.WriteHeader(204)
	})
}

func TestManagementRegistrationBypassesConsoleAndRedirects(t *testing.T) {
	probe := &registrationProbe{}
	handler := NewHandler(nil, Config{APIKey: "synthetic-model-key", AdminAuthEnabled: true, AdminUsername: "fixture", AdminPassword: "synthetic-password", Management: probe})
	for _, path := range []string{"/api/manage/v1", "/api/manage/v1/", "/api/manage/v1/unknown", "/api/manage/v1//unknown", "/api/manage/v1/accounts/../unknown"} {
		w := authExchange(handler, "POST", path, "")
		if w.Code != 204 || w.Header().Get("Location") != "" || probe.seenPath != path {
			t.Fatalf("management dispatch path=%s status=%d", path, w.Code)
		}
	}
	if w := authExchange(handler, "GET", "/health", ""); w.Code != 200 {
		t.Fatalf("health status=%d", w.Code)
	}
	if w := authExchange(handler, "GET", "/v1/models", ""); w.Code != 401 {
		t.Fatalf("model auth changed: %d", w.Code)
	}
	if w := authExchange(handler, "GET", "/api/management-keys", ""); w.Code != 403 {
		t.Fatalf("unauthenticated key access=%d", w.Code)
	}
	login := authExchange(handler, "POST", "/api/auth/login", `{"username":"fixture","password":"synthetic-password"}`)
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) != 1 {
		t.Fatalf("console login=%d", login.Code)
	}
	if w := authExchange(handler, "GET", "/api/management-keys", "", cookies[0]); w.Code != 204 {
		t.Fatalf("password session access=%d", w.Code)
	}
	authExchange(handler, "POST", "/api/auth/logout", "", cookies[0])
	if w := authExchange(handler, "GET", "/api/management-keys", "", cookies[0]); w.Code != 403 {
		t.Fatalf("revoked session access=%d", w.Code)
	}
}

func TestManagementKeysDenyDisabledLoginLoopback(t *testing.T) {
	handler := NewHandler(nil, Config{Management: &registrationProbe{}})
	for _, path := range []string{"/api/management-keys", "/api/management-keys/", "/api/management-keys//fixture"} {
		if w := authExchange(handler, "POST", path, `{}`); w.Code != 403 {
			t.Fatalf("loopback key administration: %s=%d", path, w.Code)
		}
	}
}
