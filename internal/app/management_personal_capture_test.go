package app

// Offline personal-source contract. The existing fixture uses real management
// HTTP/store/coordinator/task services and a no-network synthetic target.
import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestManagementPersonalCaptureGmailHTTPKeepsReceiptsAndZeroGeneration(t *testing.T) {
	f := newG6Fixture(t, t.TempDir())
	call := func(method, path, key string, body any, expected int) map[string]any {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, c.Namespace+path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+f.keys["admin"].Token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		f.http.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: status %d, expected %d", method, path, w.Code, expected)
		}
		var envelope map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["requestId"] != w.Header().Get("X-Request-Id") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("management response lost request identity or cache protection")
		}
		return envelope
	}
	await := func(taskID string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			task, err := f.store.GetTask(context.Background(), taskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status.Terminal() {
				if task.Status != c.TaskSucceeded {
					t.Fatalf("personal task ended with %s", task.Status)
				}
				return call("GET", "/tasks/"+taskID, "", nil, 200)["data"].(map[string]any)
			}
			if time.Now().After(deadline) {
				t.Fatal("personal task did not complete")
			}
			time.Sleep(time.Millisecond)
		}
	}
	credentials := g6Credentials("personal-capture")
	credentials.AccountName = "capture.personal.synthetic@gmail.com"
	body := map[string]any{"verify": false, "items": []any{
		map[string]any{"clientRef": "personal:7", "credentials": credentials},
	}}
	ack := call("POST", "/accounts/import", "personal-import", body, 202)["data"].(map[string]any)
	taskID := ack["taskId"].(string)
	task := await(taskID)
	item := task["items"].([]any)[0].(map[string]any)
	receipt := item["upload"]
	accountID := item["accountId"].(string)
	if !c.ValidAccountID(accountID) {
		t.Fatal("personal account did not receive a persistent UUID")
	}
	account := call("GET", "/accounts/"+accountID, "", nil, 200)["data"].(map[string]any)
	if account["accountName"] != credentials.AccountName {
		t.Fatal("ordinary Gmail identity was changed")
	}
	replay := call("POST", "/accounts/import", "personal-import", body, 202)["data"]
	if !reflect.DeepEqual(replay, ack) {
		t.Fatal("same personal request did not reuse original admission")
	}
	disabled := call("PATCH", "/accounts/"+accountID, "", map[string]any{
		"enabled": false, "expectedCredentialVersion": account["credentialVersion"],
		"expectedStateVersion": account["stateVersion"],
	}, 200)["data"].(map[string]any)
	stale := call("PATCH", "/accounts/"+accountID, "", map[string]any{
		"enabled": true, "expectedCredentialVersion": account["credentialVersion"],
		"expectedStateVersion": account["stateVersion"],
	}, 409)["error"].(map[string]any)
	if stale["code"] != "VERSION_CONFLICT" {
		t.Fatal("personal account accepted a stale state version")
	}
	credentials.Cookies[0].Value = "g6-synthetic-personal-replacement"
	replaced := call("PUT", "/accounts/"+accountID+"/credentials", "personal-replace", map[string]any{
		"credentials": credentials, "verify": false,
		"expectedCredentialVersion": disabled["credentialVersion"],
		"expectedStateVersion":      disabled["stateVersion"],
	}, 202)["data"].(map[string]any)
	await(replaced["taskId"].(string))
	current := call("GET", "/accounts/"+accountID, "", nil, 200)["data"].(map[string]any)
	if current["accountId"] != accountID || current["index"] != account["index"] || current["credentialVersion"].(float64) <= account["credentialVersion"].(float64) {
		t.Fatal("personal replacement lost identity, index, or credential version")
	}
	original := call("GET", "/tasks/"+taskID, "", nil, 200)["data"].(map[string]any)
	if !reflect.DeepEqual(original["items"].([]any)[0].(map[string]any)["upload"], receipt) {
		t.Fatal("personal replacement rewrote historical upload receipt")
	}
	metrics := f.target.snapshot()
	if metrics["generated"] != 0 || metrics["opened"] != 0 {
		t.Fatal("verify:false personal import or replacement invoked a model")
	}
}

// One exact synthetic identity permits ordinary-domain contract testing without
// accepting arbitrary Gmail credentials in this private loopback fixture.
func g6PersonalSyntheticName(name string) bool {
	return name == "capture.personal.synthetic@gmail.com"
}

func TestManagementPersonalCaptureFixtureGuardKeepsSyntheticBoundary(t *testing.T) {
	for _, sample := range []struct {
		body    string
		allowed bool
	}{
		{`{"credentials":{"accountName":"capture.personal.synthetic@gmail.com","cookies":[{"value":"g6-synthetic-one"}],"origins":[]}}`, true},
		{`{"credentials":{"accountName":"another@gmail.com","cookies":[{"value":"g6-synthetic-one"}],"origins":[]}}`, false},
		{`{"credentials":{"accountName":"capture.personal.synthetic@gmail.com","cookies":[{"value":"non-fixture-cookie"}],"origins":[]}}`, false},
		{`{"credentials":{"accountName":"capture.personal.synthetic@gmail.com","cookies":[],"origins":[{"origin":"https://aistudio.google.com"}]}}`, false},
	} {
		if g6SyntheticBody([]byte(sample.body)) != sample.allowed {
			t.Fatal("personal fixture guard accepted an unsafe input or rejected its fixed synthetic identity")
		}
	}
}

func TestManagementPersonalCaptureFixtureDiagnosticsKeepsMembershipAndPaging(t *testing.T) {
	f := newG6Fixture(t, t.TempDir())
	ctx := context.Background()
	for _, label := range []string{"membership-one", "membership-two"} {
		if _, err := f.accounts.Import(ctx, c.ImportCommand{Credentials: g6Credentials(label)}); err != nil {
			t.Fatal(err)
		}
	}
	projection := g6Projection{store: f.store}
	for _, offset := range []int{0, 1} {
		query := c.PageQuery{Limit: 1, Offset: offset}
		accounts, err := f.store.ListAccounts(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := projection.Diagnostics(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.Accounts.Total != accounts.Total || runtime.Accounts.Offset != accounts.Offset || runtime.Accounts.Limit != accounts.Limit || len(runtime.Accounts.Items) != len(accounts.Items) {
			t.Fatal("fixture runtime lost account membership or pagination metadata")
		}
		if len(runtime.Accounts.Items) != 1 || runtime.Accounts.Items[0].AccountID != accounts.Items[0].AccountID || runtime.Accounts.Items[0].State != "unavailable" {
			t.Fatal("fixture projected a different UUID or invented a ready runtime")
		}
	}
	if f.target.snapshot()["generated"] != 0 {
		t.Fatal("diagnostics called a model")
	}
}
