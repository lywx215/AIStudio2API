package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementRootDispatchDoesNotCleanPaths(t *testing.T) {
	seen := ""
	handler := rootHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.URL.Path; w.WriteHeader(204) }))
	for _, path := range []string{"/api/manage/v1", "/api/manage/v1//missing", "/api/manage/v1/accounts/../missing", "/api/management-keys//missing"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 204 || seen != path || w.Header().Get("Location") != "" {
			t.Fatalf("root dispatch %s status=%d seen=%s", path, w.Code, seen)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("security headers lost")
		}
	}
}
