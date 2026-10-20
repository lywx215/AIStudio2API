package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// This exercises the actual process composition with an empty explicit auth
// root and Go WAA. It never starts generation, opens a browser or calls a target.
func TestManagementProductionCompositionEmptyRebind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	cfg := config.Default()
	cfg.AuthStates = filepath.Join(root, "synthetic-empty-auth")
	cfg.WAABackend = config.WAABackendGo
	// An explicit, unused loopback proxy prevents consulting system proxy config.
	cfg.Proxy = "http://127.0.0.1:1"
	cfg.ProxyAPIKey = "synthetic-model-key"
	manager, err := newRuntimeManager(ctx, filepath.Join(root, "synthetic.env"), cfg, dataConfigOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	process := manager.management
	maintenance := manager.managementMaintenance
	if maintenance == nil {
		t.Fatal("process retention maintenance was not started")
	}
	key, err := process.Store.CreateKey(ctx, contract.KeyCreateRequest{Name: "synthetic", Scopes: []contract.Scope{contract.ScopeSystemRead}})
	if err != nil {
		t.Fatal(err)
	}
	handler := rootHandler(api.NewHandler(manager, api.Config{APIKey: cfg.ProxyAPIKey, Admin: manager, Management: process.HTTP}))
	request := httptest.NewRequest("GET", contract.Namespace+"/system/readiness", nil)
	request.Header.Set("Authorization", "Bearer "+key.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	var response contract.Envelope[contract.Readiness]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.Data.Ready || !contract.ValidRequestID(response.RequestID) {
		t.Fatalf("cold real composition readiness=%d", w.Code)
	}
	credentials := contract.Credentials{AccountName: "g0-composition@example.invalid", Origins: []contract.StorageOrigin{}}
	for _, name := range []string{"SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID"} {
		credentials.Cookies = append(credentials.Cookies, contract.Cookie{Name: name, Value: "synthetic-g0-cookie", Domain: ".google.com", Path: "/", Expires: -1, HTTPOnly: true, Secure: true, SameSite: "None"})
	}
	imported, err := process.Accounts.Import(ctx, contract.ImportCommand{Credentials: credentials})
	if err != nil || !imported.Published {
		t.Fatalf("synthetic local commit: %v", err)
	}
	old := manager.generationSnapshot()
	next, err := manager.buildGeneration(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Complete a new credential write after the candidate generation loaded its
	// files but before BindRuntime publishes it. Binding must synchronize this
	// newer durable state instead of exposing its construction-time snapshot.
	credentials.Cookies[0].Value = "synthetic-g0-cookie-newer"
	cv, sv := imported.Account.CredentialVersion, imported.Account.StateVersion
	replaced, err := process.Accounts.Replace(ctx, contract.ReplaceCommand{AccountID: imported.Account.AccountID, Credentials: credentials, Expected: contract.ExpectedVersions{Credential: &cv, State: &sv}, Enable: true})
	if err != nil || !replaced.Published {
		_ = next.Close()
		t.Fatalf("synthetic local replace: %v", err)
	}
	if err := manager.bindManagement(ctx, next); err != nil {
		_ = next.Close()
		t.Fatal(err)
	}
	published, err := next.managementRuntime.admin.pool.Account(imported.Account.NativeID)
	if err != nil || published.StorageState.Cookies[0].Value != "synthetic-g0-cookie-newer" {
		_ = next.Close()
		t.Fatal("binding published stale construction-time credentials")
	}
	manager.mu.Lock()
	manager.current = next
	manager.mu.Unlock()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if next.managementRuntime.Epoch() == old.managementRuntime.Epoch() {
		t.Fatal("epoch reused")
	}
	if manager.management != process {
		t.Fatal("process management was rebuilt")
	}
	if manager.managementMaintenance != maintenance {
		t.Fatal("retention maintenance was rebuilt with a generation")
	}
	if _, err := process.Store.AuthenticateKey(ctx, key.Token); err != nil {
		t.Fatal("key did not survive generation rebind")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}
