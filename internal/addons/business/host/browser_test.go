package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/browser"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func TestRemoteCleanupFailureNeverSavesAndSurvivesShutdown(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
		return &captureDriver{command: remoteFixture, close: func() error { return errors.New("private cleanup detail") }}, nil
	}
	id := startFixture(t, h, "owner", context.Background())
	w := remoteCall(h, "owner", context.Background(), "POST", "/api/addons/business/browser/"+id+"/save", map[string]any{})
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := h.Close(context.Background()); !errors.Is(err, browser.ErrCleanup) {
		t.Fatal("cleanup failure lost during shutdown", err)
	}
	s, err := h.accounts.Load()
	if err != nil || len(s.Accounts) != 0 {
		t.Fatal("failed cleanup saved credentials", err)
	}
}

func remoteFixture(ctx context.Context, method string, args map[string]any) (json.RawMessage, error) {
	switch method {
	case "browsingContext.captureScreenshot":
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 10, 10)))
		return json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(b.Bytes())})
	case "browsingContext.setViewport", "input.performActions", "script.addPreloadScript":
		return json.RawMessage(`{}`), nil
	case "script.evaluate":
		if strings.HasPrefix(args["expression"].(string), "JSON.stringify") {
			return json.RawMessage(`{"type":"success","result":{"type":"string","value":"[]"}}`), nil
		}
	}
	return readyCapture(ctx, method, args)
}
func remoteCall(h *Host, owner string, lifetime context.Context, method, path string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r = r.WithContext(contract.WithConsole(r.Context(), owner, lifetime))
	w := httptest.NewRecorder()
	h.AdminHandler().ServeHTTP(w, r)
	return w
}
func startFixture(t *testing.T, h *Host, owner string, lifetime context.Context) string {
	t.Helper()
	input := captureInput(0)
	input["headless"] = true
	w := remoteCall(h, owner, lifetime, "POST", "/api/addons/business/browser", input)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result taskView
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("invalid task")
	}
	for n := 0; n < 100; n++ {
		w = remoteCall(h, owner, lifetime, "GET", "/api/addons/business/browser/"+result.ID, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		if result.State == "ready" {
			return result.ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task not ready")
	return ""
}
func TestRemoteCollectionOwnerLogoutAndBudget(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	defer h.Close(context.Background())
	closed := make(chan struct{})
	h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
		return &captureDriver{command: remoteFixture, close: func() error { close(closed); return nil }}, nil
	}
	lifetime, cancel := context.WithCancel(context.Background())
	id := startFixture(t, h, "owner", lifetime)
	if h.budget.Snapshot().InUseBytes != 16<<20 {
		t.Fatal("missing browser reservation")
	}
	if w := remoteCall(h, "other", context.Background(), "GET", "/api/addons/business/browser/"+id+"/frame", nil); w.Code != 404 {
		t.Fatal("cross-console access", w.Code)
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("logout did not close browser")
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.budget.Snapshot().InUseBytes != 0 {
		t.Fatal("browser budget leaked")
	}
	s, err := h.accounts.Load()
	if err != nil || len(s.Accounts) != 0 {
		t.Fatal("logout saved credentials", err)
	}
}
func TestRemoteSaveCASAndCleanup(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "CAS"}[stale], func(t *testing.T) {
			h := New(filepath.Join(t.TempDir(), "config.json"), nil)
			defer h.Close(context.Background())
			cleaned := false
			h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
				return &captureDriver{command: remoteFixture, close: func() error { cleaned = true; return nil }}, nil
			}
			id := startFixture(t, h, "owner", context.Background())
			if stale {
				if _, err := h.accounts.Update(0, func(*vault.Snapshot) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			w := remoteCall(h, "owner", context.Background(), "POST", "/api/addons/business/browser/"+id+"/save", map[string]any{})
			want := 200
			if stale {
				want = 409
			}
			if w.Code != want || !cleaned || strings.Contains(w.Body.String(), "synthetic-collection-cookie") {
				t.Fatal(w.Code, w.Body.String())
			}
			s, err := h.accounts.Load()
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				if len(s.Accounts) != 0 {
					t.Fatal("stale capture saved")
				}
			} else if len(s.Accounts) != 1 || s.Accounts[0].Enabled {
				t.Fatal("saved account not disabled")
			}
		})
	}
}
