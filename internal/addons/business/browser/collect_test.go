package browser

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

const captureURL = "https://business.gemini.google/u/0/cid/synthetic-config?csesidx=123"

type fakeDriver struct {
	command func(context.Context, string, map[string]any) (json.RawMessage, error)
	close   func() error
}

func (d *fakeDriver) Command(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return d.command(ctx, method, params)
}
func (d *fakeDriver) Close() error {
	if d.close != nil {
		return d.close()
	}
	return nil
}
func rawJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
func syntheticCookie(name string) map[string]any {
	return map[string]any{"name": name, "domain": "business.gemini.google", "path": "/", "secure": true, "httpOnly": true, "value": map[string]string{"type": "string", "value": "synthetic-cookie"}}
}
func cookieJSON(items ...map[string]any) json.RawMessage {
	return rawJSON(map[string]any{"cookies": items})
}
func readyCommand(t *testing.T, methods *[]string) func(context.Context, string, map[string]any) (json.RawMessage, error) {
	t.Helper()
	return func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
		if methods != nil {
			*methods = append(*methods, method)
		}
		switch method {
		case "session.new":
			return json.RawMessage(`{"capabilities":{"browserName":"firefox","browserVersion":"152.0.1"}}`), nil
		case "browsingContext.getTree":
			return rawJSON(map[string]any{"contexts": []any{map[string]string{"context": "synthetic-context", "url": captureURL}}}), nil
		case "browsingContext.navigate":
			if params["url"] != "https://business.gemini.google/" || params["wait"] != "none" {
				t.Error("unexpected navigation target")
			}
			return json.RawMessage(`{}`), nil
		case "storage.getCookies":
			if !reflect.DeepEqual(params["partition"], map[string]string{"type": "context", "context": "synthetic-context"}) {
				t.Error("cookies must be scoped to the collection context")
			}
			return cookieJSON(syntheticCookie("__Secure-C_SES"), syntheticCookie("__Host-C_OSES")), nil
		case "script.evaluate":
			if params["expression"] != "navigator.userAgent" {
				t.Error("collector may only read the user agent")
			}
			return json.RawMessage(`{"type":"success","result":{"type":"string","value":"synthetic Firefox/152.0"}}`), nil
		default:
			t.Errorf("unapproved collection command: %s", method)
			return nil, ErrBrowser
		}
	}
}

func TestCaptureSnapshotAndCleanup(t *testing.T) {
	var methods []string
	closed := false
	d := &fakeDriver{command: readyCommand(t, &methods), close: func() error { closed = true; return nil }}
	credential, err := Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
	if err != nil || credential.Validate() != nil || !closed {
		t.Fatalf("capture/cleanup failed: %v", err)
	}
	if credential.ConfigID != "synthetic-config" || credential.CSESIndex != "123" || credential.UserAgent != "synthetic Firefox/152.0" {
		t.Error("metadata was not taken from the current context")
	}
	want := []string{"session.new", "browsingContext.getTree", "browsingContext.navigate", "browsingContext.getTree", "storage.getCookies", "script.evaluate", "browsingContext.getTree", "storage.getCookies"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("unexpected passive command sequence: %v", methods)
	}
}

func TestCaptureCleanupFailureDiscardsCredential(t *testing.T) {
	d := &fakeDriver{command: readyCommand(t, nil), close: func() error { return errors.New("synthetic secret in cleanup detail") }}
	credential, err := Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
	if !errors.Is(err, ErrCleanup) || credential != (vault.Credential{}) {
		t.Fatalf("cleanup failure was returned as success: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("raw driver cleanup error leaked")
	}
}

func TestDriverVersionStrictFirefox152(t *testing.T) {
	for _, version := range []string{"", "151.0", "152x", "152.0beta", "152.0\n", "153.0"} {
		t.Run(version, func(t *testing.T) {
			closed := false
			d := &fakeDriver{command: func(context.Context, string, map[string]any) (json.RawMessage, error) {
				return rawJSON(map[string]any{"capabilities": map[string]string{"browserName": "firefox", "browserVersion": version}}), nil
			}, close: func() error { closed = true; return nil }}
			_, err := Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
			if !errors.Is(err, ErrVersion) || !closed {
				t.Fatalf("incompatible version accepted: %v", err)
			}
		})
	}
	d := &fakeDriver{command: func(context.Context, string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"capabilities":{"browserName":"chrome","browserVersion":"152.0"}}`), nil
	}}
	if _, err := Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil }); !errors.Is(err, ErrVersion) {
		t.Error("non-Firefox browser accepted")
	}
}

func TestCaptureMissingMetadataAndCancel(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "missing"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			base := readyCommand(t, nil)
			d := &fakeDriver{command: func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
				if method == "browsingContext.getTree" {
					if !deadline {
						cancel()
					}
					return json.RawMessage(`{"contexts":[{"context":"synthetic-context","url":"https://business.gemini.google/"}]}`), nil
				}
				return base(ctx, method, params)
			}}
			credential, err := Collect(ctx, Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
			if credential != (vault.Credential{}) || deadline && !errors.Is(err, ErrMissing) || !deadline && !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong incomplete outcome: %v", err)
			}
		})
	}
}

func TestBusinessSnapshotRejectsAccountSwitch(t *testing.T) {
	for _, mutate := range []string{"url", "cookie"} {
		t.Run(mutate, func(t *testing.T) {
			base := readyCommand(t, nil)
			trees, cookieReads := 0, 0
			d := &fakeDriver{command: func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
				if method == "browsingContext.getTree" {
					trees++
					if mutate == "url" && trees == 2 {
						return json.RawMessage(`{"contexts":[{"context":"synthetic-context","url":"https://business.gemini.google/cid/other?csesidx=456"}]}`), nil
					}
				}
				if method == "storage.getCookies" {
					cookieReads++
					if mutate == "cookie" && cookieReads == 2 {
						c := syntheticCookie("__Secure-C_SES")
						c["value"] = map[string]string{"type": "string", "value": "changed-synthetic-cookie"}
						return cookieJSON(c), nil
					}
				}
				return base(ctx, method, params)
			}}
			credential, ready, err := snapshot(context.Background(), d, "synthetic-context")
			if err != nil || ready || credential != (vault.Credential{}) {
				t.Fatalf("mixed account snapshot accepted: ready=%v, err=%v", ready, err)
			}
		})
	}
}

func TestCaptureMetadataSourceAndValidation(t *testing.T) {
	for _, address := range []string{
		"http://business.gemini.google/cid/config?csesidx=123",
		"https://business.gemini.google.evil/cid/config?csesidx=123",
		"https://user@business.gemini.google/cid/config?csesidx=123",
		"https://business.gemini.google:443/cid/config?csesidx=123",
		"https://business.gemini.google/cid/config?csesidx=1&csesidx=2",
		"https://business.gemini.google/cid/config?csesidx=not-a-number",
		"https://business.gemini.google/cid/config/cid/other?csesidx=123",
		"https://business.gemini.google/cid/?csesidx=123",
		"https://business.gemini.google/cid/%2fconfig?csesidx=123",
		"https://business.gemini.google/cid/../?csesidx=123",
		"https://business.gemini.google/123/config/456?csesidx=123",
		"https://business.gemini.google/cid/config?csesidx=123&invalid=%zz",
	} {
		config, index := metadata(address)
		if config != "" || index != "" {
			t.Errorf("accepted untrusted/missing URL metadata: %s", address)
		}
	}
	if config, index := metadata(captureURL); config != "synthetic-config" || index != "123" {
		t.Fatal("valid explicit URL fields were rejected")
	}
}

func TestCookieDomainPathExpiryAndHttpOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"unrelated-domain", func(c map[string]any) { c["domain"] = "evil.google" }},
		{"parent-domain", func(c map[string]any) { c["domain"] = ".google" }},
		{"subdomain", func(c map[string]any) { c["domain"] = "other.business.gemini.google" }},
		{"insecure", func(c map[string]any) { c["secure"] = false }},
		{"invalid-path", func(c map[string]any) { c["path"] = "" }},
		{"path-prefix-only", func(c map[string]any) { c["path"] = "/u/01" }},
		{"expired", func(c map[string]any) { c["expiry"] = time.Now().Add(-time.Hour).Unix() }},
		{"zero-expiry", func(c map[string]any) { c["expiry"] = 0 }},
		{"negative-expiry", func(c map[string]any) { c["expiry"] = -1 }},
		{"non-string-value", func(c map[string]any) { c["value"] = map[string]string{"type": "base64", "value": "ignored"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := syntheticCookie("__Secure-C_SES")
			tc.change(c)
			if _, ok := cookies(cookieJSON(c), captureURL); ok {
				t.Fatal("invalid cookie selected")
			}
		})
	}
	for _, path := range []string{"/", "/u", "/u/", "/u/0"} {
		c := syntheticCookie("__Secure-C_SES")
		c["path"] = path
		c["domain"] = ".business.gemini.google"
		c["expiry"] = time.Now().Add(time.Hour).Unix()
		if _, ok := cookies(cookieJSON(c), captureURL); !ok {
			t.Errorf("valid HttpOnly cookie rejected for path %s", path)
		}
	}
	c1, c2 := syntheticCookie("__Secure-C_SES"), syntheticCookie("__Secure-C_SES")
	c2["value"] = map[string]string{"type": "string", "value": "conflicting-synthetic-cookie"}
	if _, ok := cookies(cookieJSON(c1, c2), captureURL); ok {
		t.Error("ambiguous duplicate cookie accepted")
	}
	host := syntheticCookie("__Host-C_OSES")
	host["domain"] = ".business.gemini.google"
	credential, ok := cookies(cookieJSON(c1, host), captureURL)
	if !ok || credential.HostCOSES != "" {
		t.Error("__Host cookie domain rule was ignored")
	}
	if _, ok := cookies(cookieJSON(c1), "https://evil.google/"); ok {
		t.Error("cookie selected outside Business origin")
	}
	if cookiePathMatches("/u/0", "/u/0/") || cookiePathMatches("/u/01", "/u/0") {
		t.Error("cookie path boundary rule was ignored")
	}
}
