package app

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

func TestAddonBridgeDoesNotInitializeAIStudioOrCreateStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	manager := &runtimeManager{current: &runtimeGeneration{config: config.Config{ModelMapping: "business-existing:gemini-existing"}}}
	addon, closeAddon := newBusinessAddonAt(manager, path)
	if !addon.OwnsLegacy("business-existing") || addon.OwnsLegacy("business-new") {
		t.Fatal("legacy alias classification failed")
	}
	w := httptest.NewRecorder()
	addon.Admin.ServeHTTP(w, httptest.NewRequest("GET", "/api/addons/business/status", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"disabled"`) {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("construction created storage")
	}
	closeAddon()
	closeAddon()
	w = httptest.NewRecorder()
	addon.Admin.ServeHTTP(w, httptest.NewRequest("GET", "/api/addons/business/status", nil))
	if !strings.Contains(w.Body.String(), `"state":"closed"`) {
		t.Fatal("host was not closed")
	}
}
