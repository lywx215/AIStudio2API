package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func importExchange(h *Host, path string, body any) *httptest.ResponseRecorder {
	return exchange(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.AdminHandler().ServeHTTP(w, r.WithContext(contract.WithConsole(r.Context(), "fixture-console", context.Background())))
	}), "POST", path, body)
}

// These Host cases are deliberately retained unexecuted while the user's
// Windows policy deferral applies. The pure config import tests run separately.
func TestImportPreviewApplyRollbackAndCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	h := New(path, nil)
	source := map[string]any{"models": []any{map[string]any{"id": "imported-fixture", "name": "Imported", "routing_mode": "explicit", "upstream_model_id": "captured"}}}
	previewResponse := importExchange(h, "/api/addons/business/config/import/preview", map[string]any{"revision": 0, "source": source})
	var preview config.ImportPreview
	if previewResponse.Code != 200 || json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil || len(preview.Added) != 1 {
		t.Fatal("preview failed", previewResponse.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("preview created storage")
	}
	request := map[string]any{"revision": 0, "source": source, "source_hash": preview.SourceHash}
	wrongHash := map[string]any{"revision": 0, "source": source, "source_hash": "changed"}
	if w := importExchange(h, "/api/addons/business/config/import", wrongHash); w.Code != 409 {
		t.Fatal("changed source accepted", w.Code)
	}
	response := importExchange(h, "/api/addons/business/config/import", request)
	var applied struct {
		Config     config.Config `json:"config"`
		RollbackID string        `json:"rollback_id"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &applied) != nil || applied.Config.Revision != 1 || len(applied.RollbackID) != 32 || len(applied.Config.Bindings) != 9 {
		t.Fatal("apply failed", response.Body.String())
	}
	if applied.Config.Bindings[8].Enabled || applied.Config.Enabled || h.engine != nil {
		t.Fatal("import activated runtime")
	}
	if w := importExchange(h, "/api/addons/business/config/import", request); w.Code != 409 {
		t.Fatal("stale apply accepted")
	}
	rollback := map[string]any{"revision": 1, "rollback_id": applied.RollbackID}
	w := importExchange(h, "/api/addons/business/config/import/rollback", rollback)
	var restored config.Config
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &restored) != nil || restored.Revision != 2 {
		t.Fatal("rollback failed", w.Body.String())
	}
	want := config.Default()
	want.Revision = 2
	if !reflect.DeepEqual(restored, want) {
		t.Fatal("rollback changed pre-import configuration")
	}
	if w := importExchange(h, "/api/addons/business/config/import/rollback", map[string]any{"revision": 2, "rollback_id": applied.RollbackID}); w.Code != 409 {
		t.Fatal("rollback reused")
	}
}

func TestImportRollbackNeverOverwritesSubsequentEditsOrSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	h := New(path, nil)
	source := json.RawMessage(`{"models":[{"id":"fixture","name":"Fixture"}]}`)
	p, err := config.PreviewImport(config.Default(), source)
	if err != nil {
		t.Fatal(err)
	}
	w := importExchange(h, "/api/addons/business/config/import", map[string]any{"revision": 0, "source": source, "source_hash": p.SourceHash})
	var applied struct {
		Config     config.Config `json:"config"`
		RollbackID string        `json:"rollback_id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &applied) != nil {
		t.Fatal(w.Body.String())
	}
	restarted := New(path, nil)
	if w := importExchange(restarted, "/api/addons/business/config/import/rollback", map[string]any{"revision": 1, "rollback_id": applied.RollbackID}); w.Code != 409 {
		t.Fatal("restart retained rollback capability")
	}
	applied.Config.MaxConcurrent = 3
	if w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", applied.Config); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := importExchange(h, "/api/addons/business/config/import/rollback", map[string]any{"revision": 2, "rollback_id": applied.RollbackID}); w.Code != 409 {
		t.Fatal("rollback overwrote subsequent save")
	}
	current, err := config.Load(path, nil)
	if err != nil || current.MaxConcurrent != 3 || current.Revision != 2 {
		t.Fatal("rejected rollback changed disk")
	}
}

func TestImportRequiresLiveOriginatingConsoleForUndo(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	source := json.RawMessage(`{"models":[{"id":"fixture","name":"Fixture"}]}`)
	p, err := config.PreviewImport(config.Default(), source)
	if err != nil {
		t.Fatal(err)
	}
	if w := exchange(h.AdminHandler(), "POST", "/api/addons/business/config/import/preview", map[string]any{"revision": 0, "source": source}); w.Code != 401 {
		t.Fatal("missing console accepted")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := func(id string, lifetime context.Context, path string, body any) *httptest.ResponseRecorder {
		return exchange(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.AdminHandler().ServeHTTP(w, r.WithContext(contract.WithConsole(r.Context(), id, lifetime)))
		}), "POST", path, body)
	}
	w := call("first-console", lifetime, "/api/addons/business/config/import", map[string]any{"revision": 0, "source": source, "source_hash": p.SourceHash})
	var applied struct {
		RollbackID string `json:"rollback_id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &applied) != nil {
		t.Fatal(w.Body.String())
	}
	rollback := map[string]any{"revision": 1, "rollback_id": applied.RollbackID}
	if w := call("other-console", context.Background(), "/api/addons/business/config/import/rollback", rollback); w.Code != 409 {
		t.Fatal("foreign console reused undo")
	}
	cancel()
	if w := call("first-console", context.Background(), "/api/addons/business/config/import/rollback", rollback); w.Code != 409 {
		t.Fatal("new lifetime reused expired undo")
	}
	if w := call("first-console", lifetime, "/api/addons/business/config/import/rollback", rollback); w.Code != 401 {
		t.Fatal("expired console accepted")
	}
}
