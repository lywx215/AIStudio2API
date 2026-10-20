package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func TestManagedUpdateDoesNotFetchOrInstallOfficialRelease(t *testing.T) {
	previous := managedBy
	managedBy = "lywx215"
	t.Cleanup(func() { managedBy = previous })
	var releaseCalls, restarts atomic.Int32
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		releaseCalls.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0"}`))
	}))
	defer releases.Close()
	for _, version := range []string{"(devel)", "v0.2.13", "v0.2.14-custom"} {
		if got := updateMode(version); got != "managed" {
			t.Fatalf("updateMode(%q)=%q", version, got)
		}
	}
	updater := newSelfUpdater("v0.2.14", "unused-synthetic-executable", func() string { return "" }, func(string, string) {}, func() { restarts.Add(1) })
	updater.releaseURL = releases.URL
	for _, refresh := range []bool{false, true} {
		status, err := updater.UpdateStatus(context.Background(), refresh)
		if err != nil || status.Mode != "managed" || status.ManagedBy != managedBy || status.Available || status.Latest != "" {
			t.Fatalf("managed status: %+v err=%v", status, err)
		}
	}
	_, err := updater.ApplyUpdate(context.Background())
	var conflict interface{ HTTPStatus() int }
	if !errors.As(err, &conflict) || conflict.HTTPStatus() != http.StatusConflict {
		t.Fatalf("managed ApplyUpdate error=%v", err)
	}
	handler := rootHandler(api.NewHandler(nil, api.Config{Updates: updater}))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := httptest.NewRequest(method, "http://127.0.0.1/api/update?refresh=1", nil)
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("X-Requested-With", "XMLHttpRequest")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if method == http.MethodPost {
			if response.Code != http.StatusConflict {
				t.Fatalf("managed HTTP Apply status=%d", response.Code)
			}
			continue
		}
		var status api.UpdateStatus
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Mode != "managed" || status.ManagedBy != managedBy {
			t.Fatalf("managed HTTP GET status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if releaseCalls.Load() != 0 || restarts.Load() != 0 {
		t.Fatalf("managed update touched releases=%d restart=%d", releaseCalls.Load(), restarts.Load())
	}
}
