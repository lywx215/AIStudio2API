package vault

import "testing"

func TestProxyRejectsUnsupportedAndCredentialBearingURLs(t *testing.T) {
	for _, value := range []string{"", "http://127.0.0.1:7890", "socks5://localhost:1080"} {
		if ValidateProxy(value) != nil {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"https://proxy:443", "http://user:pass@proxy:80", "http://proxy", "file:///tmp/proxy", "http://proxy:99999", "http://proxy:80?token=private", "http://proxy:80/path"} {
		if ValidateProxy(value) == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
}
