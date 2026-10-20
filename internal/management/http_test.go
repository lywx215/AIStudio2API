package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func testHTTP(t *testing.T) (c.HTTPRegistrar, *memoryStore, *fakeAccounts, *captureTasks) {
	t.Helper()
	store := newMemoryStore()
	accounts := &fakeAccounts{store: store}
	tasks := &captureTasks{}
	h, err := NewHTTP(c.HTTPOptions{Store: store, Accounts: accounts, Tasks: tasks, Projection: &fakeProjection{}, Clock: fixtureTime})
	if err != nil {
		t.Fatal(err)
	}
	return h, store, accounts, tasks
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://management.test"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer mgmt_synthetic")
	r.Header.Set("X-Request-Id", "untrusted-inbound")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assertEnvelope(t, w)
	return w
}

func assertEnvelope(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe headers: %v", w.Header())
	}
	var envelope struct {
		RequestID string   `json:"requestId"`
		Error     *c.Error `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
		t.Fatalf("non-JSON response: %q", w.Body.String())
	}
	if !c.ValidRequestID(envelope.RequestID) || envelope.RequestID != w.Header().Get("X-Request-Id") {
		t.Fatalf("invalid request proof: %q %q", envelope.RequestID, w.Header().Get("X-Request-Id"))
	}
	if envelope.Error != nil {
		spec := c.ErrorCatalog()[envelope.Error.Code]
		if spec.Message != envelope.Error.Message || spec.Status != w.Code {
			t.Fatalf("untrusted error: %d %+v", w.Code, envelope.Error)
		}
	}
}

func assertCode(t *testing.T, w *httptest.ResponseRecorder, code c.ErrorCode) {
	t.Helper()
	var envelope c.ErrorEnvelope
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error == nil || envelope.Error.Code != code {
		t.Fatalf("wanted %s, got %d %s", code, w.Code, w.Body.String())
	}
}

func importJSON(t *testing.T, verify bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"items": []map[string]any{{"clientRef": "source-1", "credentials": fixtureCredentials()}}, "verify": verify})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestHTTPNamespaceTerminalJSON(t *testing.T) {
	h, _, _, _ := testHTTP(t)
	for _, tc := range []struct {
		method, path string
		code         c.ErrorCode
		allow        string
	}{
		{"GET", c.Namespace, c.CodeNotFound, ""}, {"GET", c.Namespace + "/unknown", c.CodeNotFound, ""}, {"GET", c.Namespace + "/accounts/", c.CodeNotFound, ""}, {"GET", c.Namespace + "//accounts", c.CodeNotFound, ""}, {"GET", c.Namespace + "/./accounts", c.CodeNotFound, ""}, {"GET", c.Namespace + "/accounts%2fimport", c.CodeNotFound, ""}, {"POST", c.Namespace + "/accounts", c.CodeMethodNotAllowed, "GET"}, {"PUT", c.Namespace + "/accounts/" + fixtureAccountID, c.CodeMethodNotAllowed, "GET, PATCH"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := request(t, h.Handler(), tc.method, tc.path, "", nil)
			assertCode(t, w, tc.code)
			if w.Header().Get("Allow") != tc.allow || w.Header().Get("Location") != "" {
				t.Fatalf("unexpected route headers %v", w.Header())
			}
		})
	}
}

func TestHTTPBearerScopeAndReadiness(t *testing.T) {
	h, store, _, _ := testHTTP(t)
	for _, auth := range []string{"", "Bearer model_key", "Bearer mgmt_invalid", "Basic mgmt_synthetic", "Bearer mgmt_synthetic extra"} {
		w := request(t, h.Handler(), "GET", c.Namespace+"/system/status", "", map[string]string{"Authorization": auth})
		assertCode(t, w, c.CodeUnauthorized)
	}
	store.scopes = c.ScopeTemplate("readonly")
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), nil)
	assertCode(t, w, c.CodeForbidden)
	w = request(t, h.Handler(), "GET", c.Namespace+"/system/readiness", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":false`) || !strings.Contains(w.Body.String(), `"checks":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, endpoint := range []string{"/accounts", "/tasks", "/audit", "/usage"} {
		w = request(t, h.Handler(), "GET", c.Namespace+endpoint, "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Fatal(endpoint, w.Code, w.Body.String())
		}
	}
	w = request(t, h.Handler(), "GET", c.Namespace+"/accounts?offset=0&limit=200", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?offset=-1", "?offset=9007199254740992", "?limit=1&limit=2", "?offset=1.0", "?limit=", "?other=1"} {
		w = request(t, h.Handler(), "GET", c.Namespace+"/accounts"+query, "", nil)
		assertCode(t, w, c.CodeInvalidRequest)
	}
}

func TestHTTPPreflightBeforeIdempotencyAndAdmission(t *testing.T) {
	h, _, accounts, tasks := testHTTP(t)
	for _, body := range []string{`{"items":[]}`, `{"items":null}`, `{"items":[],"verify":null}`, `{"items":[],"items":[]}`, `{"items":[],"unknown":true}`} {
		w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", body, nil)
		assertCode(t, w, c.CodeInvalidRequest)
	}
	if len(tasks.calls) != 0 {
		t.Fatal("preflight admitted work")
	}
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), nil)
	assertCode(t, w, c.CodeIdempotencyKeyRequired)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), map[string]string{"Idempotency-Key": "one"})
	if w.Code != 202 || len(tasks.calls) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":false,"force":null}`, `{"Enabled":false}`, `{"enabled":false,"enabled":true}`, `{"enabled":false,"expectedCredentialVersion":1}`, `{"enabled":false,"expectedCredentialVersion":null,"expectedStateVersion":null}`} {
		w = request(t, h.Handler(), "PATCH", c.Namespace+"/accounts/"+fixtureAccountID, body, nil)
		assertCode(t, w, c.CodeInvalidRequest)
	}
	if accounts.patches != 0 {
		t.Fatal("invalid patches mutated account")
	}
}

func TestHTTPCanonicalDefaultsAndLegacyCredentialInput(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	headers := map[string]string{"Idempotency-Key": "canonical"}
	var original map[string]any
	_ = json.Unmarshal([]byte(importJSON(t, true)), &original)
	delete(original, "verify")
	first, _ := json.Marshal(original)
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(first), headers)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	original["verify"] = true
	original["model"] = c.DefaultModel
	item := original["items"].([]any)[0].(map[string]any)
	encoded, _ := json.Marshal(item["credentials"])
	item["credentials"] = string(encoded)
	second, _ := json.Marshal(original)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(second), headers)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if tasks.calls[0].CanonicalDigest != tasks.calls[1].CanonicalDigest {
		t.Fatal("defaults or JSON-encoded credentials changed identity")
	}
	item["clientRef"] = "source-2"
	third, _ := json.Marshal(original)
	_ = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(third), headers)
	if tasks.calls[1].CanonicalDigest == tasks.calls[2].CanonicalDigest {
		t.Fatal("different content kept identity")
	}
	credential, _ := json.Marshal(fixtureCredentials())
	legacy, _ := json.Marshal(string(credential))
	w = request(t, h.Handler(), "PUT", c.Namespace+"/accounts/"+fixtureAccountID+"/credentials", string(legacy), headers)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var input taskInput
	_ = json.Unmarshal(tasks.calls[len(tasks.calls)-1].PrivateInput, &input)
	if !input.Legacy || !input.Verify {
		t.Fatal("legacy replace lost verification")
	}
}

func TestHTTPSecretsControlsAndBounds(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	headers := map[string]string{"Idempotency-Key": "invalid"}
	credential, _ := json.Marshal(fixtureCredentials())
	for _, field := range []string{`,"disabled":true`, `,"accountId":"injected"`, `,"auth":{"token":"private-marker"}`, `,"credentialVersion":9`} {
		bad := append([]byte{}, credential[:len(credential)-1]...)
		bad = append(bad, []byte(field+"}")...)
		body := `{"verify":false,"items":[{"clientRef":"one","credentials":` + string(bad) + `}]}`
		w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", body, headers)
		assertCode(t, w, c.CodeInvalidCredentials)
		if strings.Contains(w.Body.String(), "private-marker") {
			t.Fatal("secret exposed")
		}
	}
	var obj map[string]any
	_ = json.Unmarshal([]byte(importJSON(t, false)), &obj)
	items := obj["items"].([]any)
	obj["items"] = append(items, items[0])
	body, _ := json.Marshal(obj)
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(body), headers)
	assertCode(t, w, c.CodeInvalidRequest)
	obj["items"] = items
	obj["model"] = "gemini(high)"
	body, _ = json.Marshal(obj)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(body), headers)
	assertCode(t, w, c.CodeInvalidRequest)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", strings.Repeat(" ", c.MaxBodyBytes+1), headers)
	assertCode(t, w, c.CodePayloadTooLarge)
	if len(tasks.calls) != 0 {
		t.Fatal("invalid payload admitted")
	}
	// A fake collaborator deliberately exposes no writable settings method.
	w = request(t, h.Handler(), "PATCH", c.Namespace+"/settings", `{"maxActiveWorkers":2}`, nil)
	assertCode(t, w, c.CodeInvalidState)
}

func TestKeyHTTPPasswordSessionOriginAndOneTimeToken(t *testing.T) {
	h, store, _, _ := testHTTP(t)
	body := `{"name":"integration","scopes":["system:read"]}`
	denied := h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error { return c.NewError(c.CodeConsolePasswordRequired) }))
	w := request(t, denied, "POST", c.KeyNamespace, body, map[string]string{"X-Requested-With": "XMLHttpRequest"})
	assertCode(t, w, c.CodeConsolePasswordRequired)
	allowed := h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error { return nil }))
	for _, headers := range []map[string]string{nil, {"X-Requested-With": "XMLHttpRequest", "Origin": "http://attacker.test"}, {"X-Requested-With": "XMLHttpRequest", "Origin": "https://management.test"}, {"X-Requested-With": "XMLHttpRequest", "Sec-Fetch-Site": "same-site"}, {"X-Requested-With": "XMLHttpRequest", "Sec-Fetch-Site": "cross-site"}, {"X-Requested-With": "XMLHttpRequest", "Origin": "null"}} {
		w = request(t, allowed, "POST", c.KeyNamespace, body, headers)
		assertCode(t, w, c.CodeForbidden)
	}
	if store.keyCreates != 0 {
		t.Fatal("forbidden key creation")
	}
	headers := map[string]string{"X-Requested-With": "XMLHttpRequest", "Origin": "http://management.test", "Sec-Fetch-Site": "same-origin"}
	w = request(t, allowed, "POST", c.KeyNamespace, body, headers)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"token":"mgmt_synthetic-once"`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, allowed, "GET", c.KeyNamespace, "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), "hash") {
		t.Fatal(w.Body.String())
	}
	for _, bad := range []string{`{"name":"x","scopes":["unknown"]}`, `{"name":"x","scopes":["system:read","system:read"]}`, `{"name":"x","scopes":["system:read"],"expiresAt":null}`, `{"name":"x","scopes":["system:read"],"expiresAt":"2020-01-01T00:00:00.000Z"}`} {
		w = request(t, allowed, "POST", c.KeyNamespace, bad, headers)
		assertCode(t, w, c.CodeInvalidRequest)
	}
	w = request(t, allowed, "DELETE", c.KeyNamespace+"/"+fixtureKeyID, "", headers)
	if w.Code != 200 || store.keyRevokes != 1 || strings.Contains(w.Body.String(), "token") {
		t.Fatal(w.Body.String())
	}
}

func TestHTTPKnownEndpointsAndConditionalScopes(t *testing.T) {
	h, store, accounts, tasks := testHTTP(t)
	seedAccount(store, true)
	for _, endpoint := range []string{"/system/status", "/system/readiness", "/system/capabilities", "/system/diagnostics", "/settings", "/accounts/" + fixtureAccountID} {
		w := request(t, h.Handler(), "GET", c.Namespace+endpoint, "", nil)
		if w.Code != 200 {
			t.Fatal(endpoint, w.Code, w.Body.String())
		}
	}
	w := request(t, h.Handler(), "PATCH", c.Namespace+"/accounts/"+fixtureAccountID, `{"enabled":false}`, nil)
	if w.Code != 200 || accounts.patches != 1 || !strings.Contains(w.Body.String(), `"disabledStatus":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	credential, _ := json.Marshal(fixtureCredentials())
	headers := map[string]string{"Idempotency-Key": "dispatch"}
	for _, tc := range []struct {
		method, path, body string
		kind               c.TaskKind
	}{
		{"POST", "/accounts/" + fixtureAccountID + "/archive", `{"force":false}`, c.TaskArchive},
		{"POST", "/accounts/" + fixtureAccountID + "/restore", `{}`, c.TaskRestore},
		{"POST", "/accounts/" + fixtureAccountID + "/reload", `{}`, c.TaskReload},
		{"POST", "/accounts/" + fixtureAccountID + "/test", `{"mode":"connection"}`, c.TaskTest},
		{"PUT", "/accounts/" + fixtureAccountID + "/credentials", `{"credentials":` + string(credential) + `,"verify":false,"expectedCredentialVersion":5,"expectedStateVersion":7}`, c.TaskReplace},
		{"POST", "/accounts/batch", `{"action":"disable","accountIds":["` + fixtureAccountID + `"]}`, c.TaskBatch},
		{"POST", "/system/reload-auth", `{}`, c.TaskReloadAuth},
	} {
		w = request(t, h.Handler(), tc.method, c.Namespace+tc.path, tc.body, headers)
		if w.Code != 202 || tasks.calls[len(tasks.calls)-1].Kind != tc.kind {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/export", `{"accountIds":["`+fixtureAccountID+`"]}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cookies"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, h.Handler(), "POST", c.Namespace+"/tasks/task_00000000-0000-4000-8000-000000000004/cancel", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	store.scopes = []c.Scope{c.ScopeAccountsWrite}
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, true), headers)
	assertCode(t, w, c.CodeForbidden)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), headers)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/batch", `{"action":"archive","accountIds":["`+fixtureAccountID+`"]}`, headers)
	assertCode(t, w, c.CodeForbidden)
	tasks.err = c.NewError(c.CodeRateLimited)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), headers)
	assertCode(t, w, c.CodeRateLimited)
}

func TestHTTPCredentialAndBatchByteLimits(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	headers := map[string]string{"Idempotency-Key": "bounded"}
	credential := fixtureCredentials()
	credential.Cookies[0].Value = strings.Repeat("x", c.MaxCredentialBytes)
	body, _ := json.Marshal(map[string]any{"items": []map[string]any{{"clientRef": "large", "credentials": credential}}, "verify": false})
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(body), headers)
	assertCode(t, w, c.CodePayloadTooLarge)
	items := make([]map[string]any, c.MaxBatchItems+1)
	for i := range items {
		items[i] = map[string]any{"clientRef": "one", "credentials": fixtureCredentials()}
	}
	body, _ = json.Marshal(map[string]any{"items": items, "verify": false})
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(body), headers)
	assertCode(t, w, c.CodePayloadTooLarge)
	if len(tasks.calls) != 0 {
		t.Fatal("oversize input admitted")
	}
}

func TestHTTPPublicTaskAndAccountNeverExposePrivateInput(t *testing.T) {
	h, store, _, _ := testHTTP(t)
	seedAccount(store, true)
	const taskID = "task_00000000-0000-4000-8000-000000000004"
	now := c.NewTimestamp(fixtureTime())
	store.tasks[taskID] = c.TaskRecord{Task: c.Task{TaskID: taskID, Kind: c.TaskImport, Status: c.TaskFailed, CreatedByKeyID: fixtureKeyID, CreatedAt: now, UpdatedAt: now, FinishedAt: &now, Counts: c.TaskCounts{Total: 1, Failed: 1}, Items: []c.TaskItem{{ClientRef: "source", Status: c.TaskFailed, Progress: 100, Stage: "failed", Error: &c.Error{Code: c.CodeInvalidState, Message: "private-marker"}}}}, Revision: 99, CancellationRequested: true}
	store.inputs[taskID] = json.RawMessage(`{"cookie":"private-marker"}`)
	w := request(t, h.Handler(), "GET", c.Namespace+"/tasks/"+taskID, "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-marker") || strings.Contains(w.Body.String(), "Revision") || strings.Contains(w.Body.String(), "CancellationRequested") {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), c.NewError(c.CodeInvalidState).Message) {
		t.Fatal("task error was not projected from the frozen catalog")
	}
	w = request(t, h.Handler(), "GET", c.Namespace+"/accounts/"+fixtureAccountID, "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "NativeID") || strings.Contains(w.Body.String(), "CredentialDigest") || strings.Contains(w.Body.String(), "cookies") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHTTPCaptureKeepsExplicitCASOutsideCanonicalDigest(t *testing.T) {
	h, store, _, tasks := testHTTP(t)
	seedAccount(store, true)
	credential, _ := json.Marshal(fixtureCredentials())
	body := `{"credentials":` + string(credential) + `,"verify":false,"expectedCredentialVersion":6,"expectedStateVersion":6}`
	headers := map[string]string{"Idempotency-Key": "future-client-pair"}
	first := request(t, h.Handler(), "PUT", c.Namespace+"/accounts/"+fixtureAccountID+"/credentials", body, headers)
	if first.Code != 202 {
		t.Fatal(first.Body.String())
	}
	row := store.accounts[fixtureAccountID]
	row.CredentialVersion = 6
	row.StateVersion = 8
	store.accounts[fixtureAccountID] = row
	second := request(t, h.Handler(), "PUT", c.Namespace+"/accounts/"+fixtureAccountID+"/credentials", body, headers)
	if second.Code != 202 {
		t.Fatal(second.Body.String())
	}
	if tasks.calls[0].CanonicalDigest != tasks.calls[1].CanonicalDigest {
		t.Fatal("server-side state changed canonical request identity")
	}
	for _, a := range tasks.calls {
		if a.Capture.Mode != c.TaskCaptureItems || a.Capture.Expected.Credential == nil || *a.Capture.Expected.Credential != 6 || *a.Capture.Expected.State != 6 {
			t.Fatal("client pair rebased or dropped")
		}
	}
	raw, err := json.Marshal(c.TaskWork{Record: c.TaskRecord{Task: c.Task{TaskID: "private-marker"}}, PrivateInput: json.RawMessage(`{"cookie":"private-marker"}`), PreconditionSet: &c.TaskPreconditionSet{FormatVersion: 1, Mode: c.TaskCaptureItems, TargetCount: 1}, Preconditions: []c.TaskPrecondition{{AccountID: fixtureAccountID, SnapshotKnown: true}}})
	if err != nil || string(raw) != "{}" {
		t.Fatal("private task work serialized", string(raw), err)
	}
}

func TestHTTPVerificationChannelsCanonicalAdmission(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	handler := h.Handler()
	headers := map[string]string{"Idempotency-Key": "channels"}
	for _, body := range []string{`{}`, `{"channels":["build"]}`, `{"channels":["playground","build"]}`, `{"channels":["build","playground"]}`} {
		w := request(t, handler, "POST", c.Namespace+"/accounts/"+fixtureAccountID+"/test", body, headers)
		if w.Code != http.StatusAccepted {
			t.Fatalf("body=%s %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(tasks.calls) != 4 || tasks.calls[0].CanonicalDigest != tasks.calls[1].CanonicalDigest || tasks.calls[2].CanonicalDigest != tasks.calls[3].CanonicalDigest || tasks.calls[0].CanonicalDigest == tasks.calls[2].CanonicalDigest {
		t.Fatal("channel selections did not normalize before digest")
	}
	var input taskInput
	if json.Unmarshal(tasks.calls[0].PrivateInput, &input) != nil || len(input.Channels) != 1 || input.Channels[0] != c.ChannelBuild {
		t.Fatal("default selection was not persisted")
	}
	for _, body := range []string{`{"channels":null}`, `{"channels":[]}`, `{"channels":["build","build"]}`, `{"channels":["unknown"]}`, `{"channels":"build"}`} {
		before := len(tasks.calls)
		w := request(t, handler, "POST", c.Namespace+"/accounts/"+fixtureAccountID+"/test", body, headers)
		assertCode(t, w, c.CodeInvalidRequest)
		if len(tasks.calls) != before {
			t.Fatal("invalid selection was admitted")
		}
	}
}
