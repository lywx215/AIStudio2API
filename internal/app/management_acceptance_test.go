package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func g6Credentials(label string) c.Credentials {
	cookies := []c.Cookie{}
	for _, name := range []string{"SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID"} {
		cookies = append(cookies, c.Cookie{Name: name, Value: "g6-synthetic-" + label + "-" + name, Domain: ".google.com", Path: "/", Expires: -1, HTTPOnly: true, Secure: true, SameSite: "None"})
	}
	return c.Credentials{AccountName: "g6-" + label + "@example.invalid", Cookies: cookies, Origins: []c.StorageOrigin{}}
}

func g6Expected(account c.AccountRecord) c.ExpectedVersions {
	credential, state := account.CredentialVersion, account.StateVersion
	return c.ExpectedVersions{Credential: &credential, State: &state}
}

// Use the actual G0 runtime adapter and actual native account lease/hook. Only
// workers' unstarted local registrations and an empty tracked service are needed;
// no worker process, browser, WAA, network or config loader is invoked.
func g6BindNativeRuntime(t *testing.T, fixture *g6Fixture, directory string, epoch uint64) (*managementAccountRuntime, *aistudio.AccountPool) {
	t.Helper()
	store := aistudio.NewAccountStore(filepath.Join(directory, "synthetic-auth"))
	accounts, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	pool := aistudio.NewAccountPool(accounts, 2)
	pool.SetManagementEpoch(epoch)
	pool.SetNativeWriteCoordinator(fixture.accounts, store.AuthPaths(), filepath.Join(directory, "synthetic-journal"))
	store.SetNativeWriteCoordinator(fixture.accounts, filepath.Join(directory, "synthetic-journal"))
	headers, err := newAccountHeaderProvider(accounts, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(headers.Close)
	workers := &accountWorkerManager{pool: pool, accounts: map[string]*accountWorker{}}
	for _, account := range accounts {
		workers.accounts[account.ID] = &accountWorker{id: account.ID, label: account.ID}
	}
	runtime := &managementAccountRuntime{epoch: epoch, admin: &runtimeAdmin{store: store, pool: pool, headers: headers, workers: workers, service: &trackedService{}}}
	if err := fixture.accounts.BindRuntime(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	return runtime, pool
}

func TestManagementAcceptanceNativeLeaseAvoidsSelfDrainAndKeepsDualCAS(t *testing.T) {
	directory := t.TempDir()
	fixture := newG6Fixture(t, directory)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	imported, err := fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("native")})
	if err != nil {
		t.Fatal(err)
	}
	runtime, pool := g6BindNativeRuntime(t, fixture, directory, 2)
	lease, err := pool.AcquireAccount(ctx, imported.Account.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if !lease.NativeLeaseActive() || lease.NativeLeaseEpoch() != 2 {
		t.Fatal("actual native lease has no pinned epoch")
	}
	// A real management mutation is draining this same account. The in-flight
	// native Cookie commit must bypass its waiting drain without resetting the
	// active worker or losing the pool/file/metadata version relationship.
	managementDone := make(chan error, 1)
	go func() {
		_, err := fixture.accounts.Patch(ctx, c.StateCommand{AccountID: imported.Account.AccountID, Expected: g6Expected(imported.Account), Enabled: false})
		managementDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		value, ok := runtime.gates.Load(imported.Account.NativeID)
		if ok && len(value.(chan struct{})) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("management drain did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	nativeDone := make(chan error, 1)
	go func() {
		nativeDone <- lease.MergeSetCookieHeaders([]string{"SAPISID=g6-synthetic-native-new; Domain=.google.com; Path=/; Secure; HttpOnly"}, "https://aistudio.google.com/", time.Now())
	}()
	select {
	case err := <-nativeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("native write waited for its own lease/drain")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-managementDone:
		if c.PublicError(err).Code != c.CodeVersionConflict {
			t.Fatalf("stale management CAS: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("management mutation did not finish after native release")
	}
	current, err := fixture.store.GetAccount(ctx, imported.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != imported.Account.CredentialVersion+1 || current.StateVersion != imported.Account.StateVersion || !current.Enabled {
		t.Fatal("native write lost a version or applied stale disable")
	}
	if _, ok := c.NativeLeaseFromContext(c.WithNativeLease(ctx, lease)); ok {
		t.Fatal("released lease still supplies a live proof")
	}
	if _, err := runtime.AcquireNativeMutation(ctx, lease); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("runtime accepted released native proof")
	}
	changed, err := fixture.accounts.Patch(ctx, c.StateCommand{AccountID: current.AccountID, Expected: g6Expected(current), Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Account.Enabled || changed.Account.CredentialVersion != current.CredentialVersion || changed.Account.StateVersion != current.StateVersion+1 {
		t.Fatal("fresh dual-CAS state mutation failed")
	}
	if fixture.target.snapshot()["generated"] != 0 {
		t.Fatal("native or management mutation called model")
	}
}

func TestManagementAcceptanceNativeEpochCannotWriteIntoNewGeneration(t *testing.T) {
	directory := t.TempDir()
	fixture := newG6Fixture(t, directory)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	imported, err := fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("epoch")})
	if err != nil {
		t.Fatal(err)
	}
	_, pool := g6BindNativeRuntime(t, fixture, directory, 2)
	lease, err := pool.AcquireAccount(ctx, imported.Account.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	_, _ = g6BindNativeRuntime(t, fixture, directory, 3)
	state, err := lease.ReloadStorageState()
	if err != nil {
		t.Fatal(err)
	}
	state.Cookies[0].Value = "g6-synthetic-stale-generation"
	if err := lease.SaveStorageState(state); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatalf("old generation native write accepted: %v", err)
	}
	current, err := fixture.store.GetAccount(ctx, imported.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Versions != imported.Account.Versions {
		t.Fatal("stale epoch changed persisted versions")
	}
}

func TestManagementAcceptanceNativeDeleteRetainsIdentityTombstoneAndReceipt(t *testing.T) {
	directory := t.TempDir()
	fixture := newG6Fixture(t, directory)
	ctx := context.Background()
	taskID, err := c.NewID("task")
	if err != nil {
		t.Fatal(err)
	}
	identity := c.UploadIdentity{TaskID: taskID, Kind: c.TaskImport, ItemIndex: 0}
	imported, err := fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("deleted"), Operation: c.OperationContext{Upload: &identity}})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Upload == nil {
		t.Fatal("import has no durable receipt")
	}
	participant := &aistudio.AccountFileParticipant{AuthPaths: []string{filepath.Join(directory, "synthetic-auth")}, JournalDir: filepath.Join(directory, "synthetic-journal")}
	deleted, err := fixture.accounts.CommitNative(ctx, c.CommitIntent{Kind: c.MutationDelete, AccountID: imported.Account.AccountID, NativeID: imported.Account.NativeID, Expected: g6Expected(imported.Account), CredentialDigest: imported.Account.CredentialDigest}, participant)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Account.Deleted {
		t.Fatal("native hard delete was not durable")
	}
	if _, err := fixture.store.GetAccount(ctx, imported.Account.AccountID); c.PublicError(err).Code != c.CodeNotFound {
		t.Fatal("hard-deleted account still publicly visible")
	}
	receipt, err := fixture.store.GetUploadReceipt(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || *receipt != *imported.Upload {
		t.Fatal("native delete changed original receipt")
	}
	other, err := fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("next")})
	if err != nil {
		t.Fatal(err)
	}
	if other.Account.Index <= imported.Account.Index || other.Account.AccountID == imported.Account.AccountID {
		t.Fatal("deleted identity/index reused")
	}
	_, err = fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("deleted")})
	if !errors.As(err, new(*c.Error)) || c.PublicError(err).Code != c.CodeDuplicateAccount {
		t.Fatalf("deleted identity tombstone not retained: %v", err)
	}
}

func TestManagementAcceptanceFixtureRejectsRecognizableRealCredentials(t *testing.T) {
	for _, raw := range []string{
		`{"credentials":{"accountName":"actual@example.com","cookies":[],"origins":[]}}`,
		`{"credentials":{"accountName":"g6-one@example.invalid","cookies":[{"value":"actual-cookie"}],"origins":[]}}`,
		`{"credentials":"{\"accountName\":\"actual@example.com\",\"cookies\":[],\"origins\":[]}"}`,
	} {
		if g6SyntheticBody([]byte(raw)) {
			t.Fatal("fixture allowed recognizable non-synthetic input")
		}
	}
	if !g6SyntheticBody([]byte(`{"items":[]}`)) || !g6SyntheticBody([]byte(`{`)) {
		t.Fatal("fixture intercepted structurally invalid request before real parser")
	}
	if !strings.HasSuffix(g6Credentials("safe").AccountName, "@example.invalid") {
		t.Fatal("unexpected fixture source identity")
	}
}

func TestManagementAcceptanceConcurrentNativeResponsesKeepNewCookiesAndManualDisable(t *testing.T) {
	directory := t.TempDir()
	fixture := newG6Fixture(t, directory)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	imported, err := fixture.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials("responses")})
	if err != nil {
		t.Fatal(err)
	}
	_, pool := g6BindNativeRuntime(t, fixture, directory, 2)
	selection := aistudio.AccountSelection{AccountID: imported.Account.NativeID}
	newer, err := pool.AcquireFor(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newer.Release() })
	older, err := pool.AcquireFor(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = older.Release() })
	merge := func(lease *aistudio.AccountLease, value string) {
		t.Helper()
		if err := lease.MergeSetCookieHeaders([]string{"SAPISID=" + value + "; Domain=.google.com; Path=/; Secure; HttpOnly"}, "https://aistudio.google.com/", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	merge(newer, "g6-synthetic-newer-response")
	merge(older, "g6-synthetic-late-old-response")
	current, material, err := fixture.accounts.SnapshotCredentials(ctx, imported.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(material.StorageState, []byte("g6-synthetic-late-old-response")) || !bytes.Contains(material.StorageState, []byte("g6-synthetic-newer-response")) {
		t.Fatal("older active response overwrote newer cookie")
	}
	if current.CredentialVersion != imported.Account.CredentialVersion+1 {
		t.Fatal("discarded stale cookie advanced credentials")
	}
	configuration := newer.Account().Config
	configuration.Enabled = false
	if err := newer.SaveConfig(configuration); err != nil {
		t.Fatal(err)
	}
	merge(older, "g6-synthetic-after-manual-disable")
	after, material, err := fixture.accounts.SnapshotCredentials(ctx, current.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || after.DisabledReason == nil || *after.DisabledReason != "manual" || after.StateVersion != current.StateVersion+1 || after.CredentialVersion != current.CredentialVersion {
		t.Fatal("late native response changed newer manual disable/versions")
	}
	if bytes.Contains(material.StorageState, []byte("g6-synthetic-after-manual-disable")) {
		t.Fatal("late cookie persisted after manual disable")
	}
}

func TestManagementAcceptanceRealHandlerResponsesMatchFrozenSchemas(t *testing.T) {
	fixture := newG6Fixture(t, t.TempDir())
	raw, err := os.ReadFile("../../docs/management/openapi.json")
	if os.IsNotExist(err) {
		raw, err = os.ReadFile("docs/management/openapi.json")
	}
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const resource = "https://g6-schema.invalid/openapi.json"
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatal(err)
	}
	compiled := map[string]*jsonschema.Schema{}
	request := func(method, path, schema string, body any, expected int) map[string]any {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		r.Header.Set("Authorization", "Bearer "+fixture.keys["admin"].Token)
		r.Header.Set("Idempotency-Key", "g6-schema-"+path)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		fixture.http.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s status=%d want=%d", method, path, w.Code, expected)
		}
		parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		s := compiled[schema]
		if s == nil {
			s, err = compiler.Compile(resource + "#/components/schemas/" + schema)
			if err != nil {
				t.Fatal(err)
			}
			compiled[schema] = s
		}
		if err := s.Validate(parsed); err != nil {
			t.Fatalf("real %s response violates schema %s: %v", path, schema, err)
		}
		payload := parsed.(map[string]any)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-Id") != payload["requestId"] {
			t.Fatal("real headers differ from schema-valid envelope")
		}
		return payload
	}
	for _, test := range []struct{ path, schema string }{
		{"/accounts", "AccountPageEnvelope"}, {"/tasks", "TaskPageEnvelope"}, {"/system/status", "SystemStatusEnvelope"},
		{"/system/readiness", "ReadinessEnvelope"}, {"/system/capabilities", "CapabilitiesEnvelope"}, {"/settings", "SettingsSnapshotEnvelope"},
		{"/system/diagnostics", "DiagnosticsEnvelope"}, {"/usage", "UsagePageEnvelope"}, {"/audit", "AuditPageEnvelope"},
	} {
		request("GET", c.Namespace+test.path, test.schema, nil, 200)
	}
	request("POST", c.Namespace+"/accounts/import", "ErrorEnvelope", map[string]any{"items": []any{}}, 400)
	ack := request("POST", c.Namespace+"/accounts/import", "TaskAcceptedEnvelope", map[string]any{"verify": false, "items": []any{map[string]any{"clientRef": "g6-schema", "credentials": g6Credentials("schema")}}}, 202)
	taskID := ack["data"].(map[string]any)["taskId"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := fixture.store.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status.Terminal() {
			if task.Status != c.TaskSucceeded {
				t.Fatal("schema task did not succeed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("schema task timed out")
		}
		time.Sleep(time.Millisecond)
	}
	task := request("GET", c.Namespace+"/tasks/"+taskID, "TaskEnvelope", nil, 200)
	item := task["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	accountID := item["accountId"].(string)
	request("GET", c.Namespace+"/accounts/"+accountID, "AccountEnvelope", nil, 200)
	request("GET", c.Namespace+"/accounts", "AccountPageEnvelope", nil, 200)
	request("GET", c.Namespace+"/tasks", "TaskPageEnvelope", nil, 200)
	request("GET", c.Namespace+"/audit", "AuditPageEnvelope", nil, 200)
}

func TestManagementAcceptanceGoWAAPreparationDefersBrowserLogin(t *testing.T) {
	requests := &requestRegistry{console: make(chan api.AdminLog, 1)}
	cfg := config.Default()
	cfg.WAABackend = config.WAABackendGo
	path, driver, err := prepareWAABackend(context.Background(), cfg, requests, 0, "")
	if err != nil || path != "" {
		t.Fatalf("Go WAA prep unexpectedly requires browser: %v", err)
	}
	lazy, ok := driver.(*lazyLoginDriver)
	if !ok || lazy.driver != nil {
		t.Fatal("Go WAA prep eagerly resolved real browser/login")
	}
}
