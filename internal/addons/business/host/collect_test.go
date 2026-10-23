package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/browser"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type captureDriver struct {
	command func(context.Context, string, map[string]any) (json.RawMessage, error)
	close   func() error
}

func (d *captureDriver) Command(ctx context.Context, method string, args map[string]any) (json.RawMessage, error) {
	return d.command(ctx, method, args)
}
func (d *captureDriver) Close() error {
	if d.close != nil {
		return d.close()
	}
	return nil
}
func readyCapture(_ context.Context, method string, _ map[string]any) (json.RawMessage, error) {
	switch method {
	case "session.new":
		return json.RawMessage(`{"capabilities":{"browserName":"camoufox","browserVersion":"152.0.4-beta.29"}}`), nil
	case "browsingContext.getTree":
		return json.RawMessage(`{"contexts":[{"context":"fixture","url":"https://business.gemini.google/u/0/cid/config-fixture?csesidx=123"}]}`), nil
	case "browsingContext.navigate":
		return json.RawMessage(`{}`), nil
	case "storage.getCookies":
		return json.RawMessage(`{"cookies":[{"name":"__Secure-C_SES","domain":"business.gemini.google","path":"/","secure":true,"httpOnly":true,"value":{"type":"string","value":"synthetic-collection-cookie"}}]}`), nil
	case "script.evaluate":
		return json.RawMessage(`{"type":"success","result":{"type":"string","value":"synthetic Firefox/152.0"}}`), nil
	default:
		return nil, browser.ErrBrowser
	}
}
func captureInput(revision uint64) map[string]any {
	executable, _ := filepath.Abs("synthetic-browser.exe")
	return map[string]any{"revision": revision, "label": "Fixture capture", "executable_path": executable}
}

func TestCollectionCommitsOnlyAfterCleanupAndSavesDisabled(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "business", "config.json"), nil)
	defer h.Close(context.Background())
	cleaned := false
	h.launcher = func(_ context.Context, options browser.Options) (browser.Driver, error) {
		if !filepath.IsAbs(options.Directory) {
			t.Fatal("collector received a relative profile root")
		}
		return &captureDriver{command: readyCapture, close: func() error {
			snapshot, err := h.accounts.Load()
			if err != nil || len(snapshot.Accounts) != 0 {
				t.Fatal("credentials persisted before browser cleanup")
			}
			cleaned = true
			return nil
		}}, nil
	}
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/collect", captureInput(0))
	if w.Code != 200 || !cleaned || strings.Contains(w.Body.String(), "synthetic-collection-cookie") {
		t.Fatal(w.Code, w.Body.String())
	}
	snapshot, err := h.accounts.Load()
	if err != nil || snapshot.Revision != 1 || len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Enabled || snapshot.Accounts[0].Epoch != 1 {
		t.Fatal("incorrect captured account state", err)
	}
	if snapshot.Accounts[0].Credential.SecureCSES != "synthetic-collection-cookie" {
		t.Fatal("credential snapshot was lost")
	}
}

func TestCollectionCASRejectsChangesWhileBrowserOpen(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	defer h.Close(context.Background())
	h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
		return &captureDriver{command: readyCapture, close: func() error {
			_, err := h.accounts.Update(0, func(s *vault.Snapshot) error { return nil })
			return err
		}}, nil
	}
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/collect", captureInput(0))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	snapshot, err := h.accounts.Load()
	if err != nil || snapshot.Revision != 1 || len(snapshot.Accounts) != 0 {
		t.Fatal("stale capture overwrote vault", err)
	}
}

func TestCollectionCleanupFailureWinsOverVersionAndNeverSaves(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
		return &captureDriver{command: func(context.Context, string, map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"capabilities":{"browserName":"firefox","browserVersion":"151.0"}}`), nil
		}, close: func() error { return errors.New("private cleanup detail") }}, nil
	}
	w := exchange(h.AdminHandler(), "POST", "/api/addons/business/collect", captureInput(0))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "cleanup") || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	snapshot, err := h.accounts.Load()
	if err != nil || snapshot.Revision != 0 || len(snapshot.Accounts) != 0 {
		t.Fatal("failed cleanup persisted credentials", err)
	}
	if err := h.Close(context.Background()); !errors.Is(err, browser.ErrCleanup) {
		t.Fatal("cleanup failure was lost", err)
	}
}

func TestCollectionRequestCancelAndHostShutdownNeverSave(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "host"}[shutdown], func(t *testing.T) {
			h := New(filepath.Join(t.TempDir(), "config.json"), nil)
			defer h.Close(context.Background())
			entered, cleaned := make(chan struct{}), make(chan struct{})
			h.launcher = func(context.Context, browser.Options) (browser.Driver, error) {
				return &captureDriver{command: func(ctx context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}, close: func() error { close(cleaned); return nil }}, nil
			}
			data, _ := json.Marshal(captureInput(0))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, "/api/addons/business/collect", strings.NewReader(string(data))).WithContext(ctx)
			done := make(chan int, 1)
			go func() { w := httptest.NewRecorder(); h.AdminHandler().ServeHTTP(w, r); done <- w.Code }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("collector not entered")
			}
			if shutdown {
				if err := h.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case code := <-done:
				if code != 499 {
					t.Fatal(code)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not finish")
			}
			select {
			case <-cleaned:
			default:
				t.Fatal("browser cleanup missing")
			}
			snapshot, err := h.accounts.Load()
			if err != nil || snapshot.Revision != 0 || len(snapshot.Accounts) != 0 {
				t.Fatal("cancelled collection persisted", err)
			}
		})
	}
}
