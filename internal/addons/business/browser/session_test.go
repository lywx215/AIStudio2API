package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionDriver struct {
	mu      sync.Mutex
	methods []string
	closed  bool
	version string
	fail    string
}

func (d *sessionDriver) Close() error { d.mu.Lock(); d.closed = true; d.mu.Unlock(); return nil }
func (d *sessionDriver) Command(_ context.Context, method string, args map[string]any) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.methods = append(d.methods, method)
	if method == d.fail {
		return nil, ErrBrowser
	}
	switch method {
	case "session.new":
		v := d.version
		if v == "" {
			v = "152.0.4-beta.29"
		}
		return json.Marshal(map[string]any{"capabilities": map[string]string{"browserName": "camoufox", "browserVersion": v}})
	case "browsingContext.getTree":
		return json.RawMessage(`{"contexts":[{"context":"test","url":"https://business.gemini.google/"}]}`), nil
	case "browsingContext.captureScreenshot":
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 10, 10)))
		return json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(b.Bytes())})
	case "script.evaluate":
		value := "fixture Firefox/152.0"
		if strings.HasPrefix(args["expression"].(string), "JSON.stringify") {
			value = "[]"
		}
		return json.Marshal(map[string]any{"type": "success", "result": map[string]string{"type": "string", "value": value}})
	case "storage.getCookies":
		return json.RawMessage(`{"cookies":[]}`), nil
	default:
		return json.RawMessage(`{}`), nil
	}
}
func TestSessionCapabilityGateAndManualInput(t *testing.T) {
	d := &sessionDriver{}
	s, err := Open(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	view, err := s.State(context.Background())
	if err != nil || view.State != "waiting" || len(view.MissingFields) != 3 {
		t.Fatal(view, err)
	}
	for _, input := range []Input{{Type: "click", X: 10, Y: 20}, {Type: "key", Key: "Tab"}, {Type: "text", Text: "manual"}} {
		if err = s.Input(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []Input{{Type: "eval", Text: "secret"}, {Type: "click", X: 1280}, {Type: "key", Key: "Control"}} {
		if err = s.Input(context.Background(), input); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
	for _, method := range d.methods {
		if strings.Contains(method, "network") || strings.Contains(method, "fetch") {
			t.Fatal("automatic network operation", method)
		}
	}
}
func TestSessionRejectsUnknownVersionAndMissingCapability(t *testing.T) {
	for _, d := range []*sessionDriver{{version: "153.0"}, {fail: "script.addPreloadScript"}, {fail: "input.performActions"}, {fail: "browsingContext.captureScreenshot"}} {
		if s, err := Open(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil }); err == nil || s != nil || !d.closed {
			t.Fatal("unsupported browser not closed", err)
		}
	}
}

// Opt-in local OS test. Uses only an inline fixture, no login/cookies/Google.
func TestRemoteBrowserLiveHeadless(t *testing.T) {
	executable := os.Getenv("BUSINESS_BROWSER_TEST_EXECUTABLE")
	if executable == "" {
		t.Skip("live browser opt-in is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	directory := t.TempDir()
	d, err := Launch(ctx, Options{ExecutablePath: executable, Directory: directory, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	raw, err := d.Command(ctx, "session.new", map[string]any{"capabilities": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var caps struct {
		Capabilities struct {
			Name    string `json:"browserName"`
			Version string `json:"browserVersion"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &caps) != nil || len(caps.Capabilities.Name) > 32 || len(caps.Capabilities.Version) > 32 {
		t.Fatal("invalid browser capabilities")
	}
	t.Logf("candidate browser reported name: %q version: %q; this test does not change the runtime allowlist", caps.Capabilities.Name, caps.Capabilities.Version)
	id, _, err := page(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []struct {
		name string
		args map[string]any
	}{
		{"browsingContext.setViewport", map[string]any{"context": id, "viewport": map[string]int{"width": 1280, "height": 800}, "devicePixelRatio": 1}},
		{"script.addPreloadScript", map[string]any{"functionDeclaration": observerScript, "contexts": []string{id}}},
		{"input.performActions", map[string]any{"context": id, "actions": []any{}}},
		{"browsingContext.navigate", map[string]any{"context": id, "url": "data:text/html,<input autofocus id=test><p>Business remote fixture</p>", "wait": "complete"}},
	} {
		if _, err = d.Command(ctx, command.name, command.args); err != nil {
			t.Fatal(command.name, err)
		}
	}
	s := &Session{driver: d, id: id}
	if err = s.Input(ctx, Input{Type: "click", X: 30, Y: 15}); err != nil {
		t.Fatal("pointer input failed", err)
	}
	if err = s.Input(ctx, Input{Type: "text", Text: "HEADLESS_OK"}); err != nil {
		t.Fatal(err)
	}
	if err = s.Input(ctx, Input{Type: "key", Key: "Tab"}); err != nil {
		t.Fatal("keyboard source switch failed", err)
	}
	if _, err = s.Frame(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err = d.Command(ctx, "script.evaluate", map[string]any{"expression": "document.getElementById('test').value", "target": map[string]string{"context": id}, "awaitPromise": false})
	if err != nil || !strings.Contains(string(raw), "HEADLESS_OK") {
		t.Fatal("manual input roundtrip failed", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("profile not cleaned", err)
	}
	if filepath.Base(executable) == "" {
		t.Fatal("invalid executable")
	}
}
