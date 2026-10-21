package management

import (
	"context"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"strings"
	"testing"
)

type totpHTTPAccounts struct {
	*fakeAccounts
	calls int
}

func (a *totpHTTPAccounts) LoginTotp(context.Context, string) (c.LoginTotpStatus, error) {
	return c.LoginTotpStatus{Configured: true, Version: 1}, nil
}
func (a *totpHTTPAccounts) WriteLoginTotp(_ context.Context, _ string, in c.LoginTotpInput, clear bool) (c.LoginTotpStatus, error) {
	a.calls++
	return c.LoginTotpStatus{Configured: !clear, Version: 2, LastOperationID: in.OperationID}, nil
}
func TestLoginTotpHTTPIsPrivateAndNeverCreatesTasks(t *testing.T) {
	_, store, accounts, tasks := testHTTP(t)
	passwords := &totpHTTPAccounts{fakeAccounts: accounts}
	h, err := NewHTTP(c.HTTPOptions{Store: store, Accounts: passwords, Tasks: tasks, Projection: &fakeProjection{}})
	if err != nil {
		t.Fatal(err)
	}
	path := c.Namespace + "/accounts/" + fixtureAccountID + "/login-totp"
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		body := `{"email":"synthetic@example.test","expectedVersion":1,"operationId":"10000000-0000-4000-8000-000000000001"}`
		if method == "PUT" {
			body = strings.TrimSuffix(body, "}") + `,"twofa_secret":"private-password-marker"}`
		}
		w := request(t, h.Handler(), method, path, body, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-password-marker") {
			t.Fatal("password response invalid or not private")
		}
	}
	if passwords.calls != 2 || len(tasks.calls) != 0 {
		t.Fatal("password update entered task persistence")
	}
	w := request(t, h.Handler(), "PUT", path, `{"email":"synthetic@example.test","expectedVersion":1,"operationId":"x","unexpected":"private-password-marker"}`, nil)
	assertCode(t, w, c.CodeInvalidRequest)
	store.scopes = c.ScopeTemplate("readonly")
	w = request(t, h.Handler(), "PUT", path, `{}`, nil)
	assertCode(t, w, c.CodeForbidden)
	if w := request(t, h.Handler(), "GET", path, "", nil); w.Code != 200 {
		t.Fatal("readonly password metadata rejected")
	}
	w = request(t, h.Handler(), "GET", path, "", map[string]string{"Authorization": "Bearer model_key"})
	assertCode(t, w, c.CodeUnauthorized)
}
