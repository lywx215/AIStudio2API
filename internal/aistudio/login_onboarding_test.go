package aistudio

import (
	"testing"
	"time"
)

func TestNativeLoginOptionsCarryOnlyCurrentTermsPermission(t *testing.T) {
	driver := &NativeLoginDriver{camoufox: "synthetic-camoufox", timeout: time.Minute}
	request := IsolatedLoginRequest{
		AccountID: "user@example.test", Password: "synthetic-password", TotpSecret: "synthetic-totp",
		Directory: t.TempDir(), Proxy: "socks5://127.0.0.1:1080", Locale: "en-US", Timezone: "America/Los_Angeles", AcceptTerms: true,
	}
	options := driver.options(request)
	if !options.AcceptTerms || options.Email != request.AccountID || options.Password != request.Password || options.TotpSecret != request.TotpSecret || options.Proxy != request.Proxy || options.Locale != request.Locale || options.Timezone != request.Timezone {
		t.Fatal("native login lost permission, credentials or account environment")
	}
	if driver.options(IsolatedLoginRequest{}).AcceptTerms {
		t.Fatal("driver retained a previous login permission")
	}
}
