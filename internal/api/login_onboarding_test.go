package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

type onboardingAdmin struct {
	AdminService
	permissions []bool
	created     AccountCreateInput
}

func (admin *onboardingAdmin) LoginAccount(_ context.Context, id string, input AccountLoginInput, _ *camoufoxnative.LiveView) (AdminAccount, error) {
	admin.permissions = append(admin.permissions, input.AcceptTerms)
	return AdminAccount{ID: id}, nil
}

func (admin *onboardingAdmin) CreateAccount(_ context.Context, input AccountCreateInput, _ *camoufoxnative.LiveView) (AdminAccount, error) {
	admin.permissions = append(admin.permissions, input.AcceptTerms)
	admin.created = input
	return AdminAccount{ID: "new@example.test"}, nil
}

func TestBrowserLoginTermsPermissionIsExplicitAndNotSticky(t *testing.T) {
	admin := &onboardingAdmin{}
	handler := NewHandler(nil, Config{Admin: admin})
	for i, body := range []string{"", `{}`, `{"accept_terms":true}`, `{}`, `{"accept_terms":false}`} {
		got := authExchange(handler, "POST", "/api/accounts/user@example.test/login", body)
		if got.Code != http.StatusOK || len(admin.permissions) != i+1 || admin.permissions[i] != (i == 2) {
			t.Fatalf("request %d: status=%d permissions=%v", i, got.Code, admin.permissions)
		}
	}
	for _, body := range []string{`{"accept_terms":"true"}`, `{"accept_terms":1}`} {
		got := authExchange(handler, "POST", "/api/accounts/user@example.test/login", body)
		if got.Code != http.StatusBadRequest || len(admin.permissions) != 5 {
			t.Fatalf("invalid permission reached login backend: status=%d calls=%d", got.Code, len(admin.permissions))
		}
	}
}

func TestCreateAccountCarriesTermsPermissionWithoutChangingEnvironment(t *testing.T) {
	admin := &onboardingAdmin{}
	handler := NewHandler(nil, Config{Admin: admin})
	got := authExchange(handler, "POST", "/api/accounts", `{"accept_terms":true,"proxy":"socks5://127.0.0.1:1080","locale":"en-US","timezone":"America/Los_Angeles"}`)
	if got.Code != http.StatusCreated || !admin.created.AcceptTerms || admin.created.Proxy != "socks5://127.0.0.1:1080" || admin.created.Locale != "en-US" || admin.created.Timezone != "America/Los_Angeles" {
		t.Fatalf("account environment or permission lost: status=%d", got.Code)
	}
	got = authExchange(handler, "POST", "/api/accounts", `{}`)
	if got.Code != http.StatusCreated || admin.created.AcceptTerms {
		t.Fatal("new-account permission carried over to another request")
	}
}
