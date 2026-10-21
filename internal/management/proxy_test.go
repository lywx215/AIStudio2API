package management

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type proxyTaskAccounts struct {
	*fakeAccounts
	proxy chan string
}

func (a proxyTaskAccounts) proof(upload *c.UploadIdentity, proxy *string, result c.AccountCommitResult) c.AccountCommitResult {
	if proxy != nil {
		a.proxy <- *proxy
		a.store.mu.Lock()
		receipt := a.store.receipts[*upload]
		receipt.ProxyApplied = true
		a.store.receipts[*upload] = receipt
		a.store.mu.Unlock()
		result.Upload = &receipt
	}
	return result
}
func (a proxyTaskAccounts) Import(ctx context.Context, command c.ImportCommand) (c.AccountCommitResult, error) {
	result, err := a.fakeAccounts.Import(ctx, command)
	return a.proof(command.Operation.Upload, command.Proxy, result), err
}
func (a proxyTaskAccounts) Replace(ctx context.Context, command c.ReplaceCommand) (c.AccountCommitResult, error) {
	result, err := a.fakeAccounts.Replace(ctx, command)
	return a.proof(command.Operation.Upload, command.Proxy, result), err
}

func TestProxyTaskForwardsPrivateAssignmentWithoutModelCalls(t *testing.T) {
	for _, kind := range []c.TaskKind{c.TaskImport, c.TaskReplace} {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryStore()
			seedAccount(store, false)
			accounts := proxyTaskAccounts{fakeAccounts: &fakeAccounts{store: store}, proxy: make(chan string, 1)}
			verifier := &fakeVerifier{}
			service, err := NewTaskService(c.TaskServiceOptions{Store: store, Accounts: accounts, Verifier: verifier})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = service.Close(ctx)
			})
			proxy := "socks5://fixture:synthetic-proxy@proxy.invalid:1080"
			credential, state := uint64(5), uint64(6)
			input := taskInput{Model: c.DefaultModel, Verify: false, Credentials: []c.Credentials{fixtureCredentials()}, Proxies: []*string{&proxy}, Expected: c.ExpectedVersions{Credential: &credential, State: &state}}
			accepted, err := service.Submit(context.Background(), admission(t, kind, "proxy-task", input))
			if err != nil {
				t.Fatal(err)
			}
			record := awaitTerminal(t, store, accepted.TaskID)
			if record.Status != c.TaskSucceeded || record.Items[0].Upload == nil || !record.Items[0].Upload.ProxyApplied || verifier.calls != 0 {
				t.Fatal("proxy task lost receipt or invoked model")
			}
			if <-accounts.proxy != proxy {
				t.Fatal("proxy changed in task execution")
			}
			if kind == c.TaskReplace {
				current, _ := store.GetAccount(context.Background(), fixtureAccountID)
				if current.Enabled {
					t.Fatal("proxy replacement enabled disabled account")
				}
			}
			public, _ := json.Marshal(record.Task)
			if strings.Contains(string(public), "synthetic-proxy") {
				t.Fatal("task query leaked proxy authentication")
			}
		})
	}
}

func TestProxyAdmissionIsPrivateAndIncludedInIdempotency(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	var body map[string]any
	if err := json.Unmarshal([]byte(importJSON(t, false)), &body); err != nil {
		t.Fatal(err)
	}
	item := body["items"].([]any)[0].(map[string]any)
	proxy := "socks5://fixture:synthetic-proxy-password@proxy.invalid:1080"
	item["proxy"] = proxy
	data, _ := json.Marshal(body)
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(data), map[string]string{"Idempotency-Key": "proxy-one"})
	if w.Code != http.StatusAccepted || strings.Contains(w.Body.String(), "synthetic-proxy-password") {
		t.Fatal("proxy admission failed or leaked private input")
	}
	if len(tasks.calls) != 1 {
		t.Fatal("missing admission")
	}
	var input taskInput
	if err := json.Unmarshal(tasks.calls[0].PrivateInput, &input); err != nil || input.proxy(0) == nil || *input.proxy(0) != proxy {
		t.Fatal("private proxy was dropped", err)
	}
	first := tasks.calls[0].CanonicalDigest
	item["proxy"] = "socks5://another.invalid:1081"
	data, _ = json.Marshal(body)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(data), map[string]string{"Idempotency-Key": "proxy-two"})
	if w.Code != http.StatusAccepted || tasks.calls[1].CanonicalDigest == first {
		t.Fatal("proxy missing from canonical identity")
	}
	item["proxy"] = "socks5h://fixture:synthetic-proxy-password@proxy.invalid:1080"
	data, _ = json.Marshal(body)
	w = request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", string(data), map[string]string{"Idempotency-Key": "proxy-invalid"})
	assertCode(t, w, c.CodeInvalidRequest)
	if len(tasks.calls) != 2 || strings.Contains(w.Body.String(), "synthetic-proxy-password") {
		t.Fatal("invalid proxy admitted or echoed")
	}
}

func TestProxyOmissionPreservesLegacyPrivateInput(t *testing.T) {
	h, _, _, tasks := testHTTP(t)
	w := request(t, h.Handler(), "POST", c.Namespace+"/accounts/import", importJSON(t, false), map[string]string{"Idempotency-Key": "without-proxy"})
	if w.Code != http.StatusAccepted || strings.Contains(string(tasks.calls[0].PrivateInput), `"proxies"`) {
		t.Fatal("omission changed old task identity")
	}
}
