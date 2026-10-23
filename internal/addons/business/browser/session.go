package browser

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"sync"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

//go:embed observer.js
var observerScript string

// This exact release passed local Windows headless screenshot/input/cleanup
// checks. New releases require explicit capability testing before admission.
var TestedVersions = []string{"camoufox 152.0.4-beta.29"}

func supportedBrowser(name, version string) bool {
	return name == "camoufox" && version == "152.0.4-beta.29"
}

type View struct {
	State         string   `json:"state"`
	MissingFields []string `json:"missing_fields"`
}
type Input struct {
	Type string `json:"type"`
	X    int    `json:"x,omitempty"`
	Y    int    `json:"y,omitempty"`
	Key  string `json:"key,omitempty"`
	Text string `json:"text,omitempty"`
}
type Observation struct {
	ConfigID  string          `json:"config_id"`
	CSESIndex string          `json:"csesidx"`
	Source    string          `json:"source"`
	URL       string          `json:"url"`
	Payload   json.RawMessage `json:"payload"`
}
type Session struct {
	mu      sync.Mutex
	driver  Driver
	id      string
	options Options
	once    sync.Once
	err     error
}

func Open(ctx context.Context, options Options, launch Launcher) (s *Session, err error) {
	if launch == nil {
		launch = Launch
	}
	d, err := launch(ctx, options)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrBrowser
	}
	s = &Session{driver: d, options: options}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
			s = nil
		}
	}()
	raw, err := d.Command(ctx, "session.new", map[string]any{"capabilities": map[string]any{}})
	if err != nil {
		return s, ErrVersion
	}
	var result struct {
		Capabilities struct {
			Name    string `json:"browserName"`
			Version string `json:"browserVersion"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &result) != nil || !supportedBrowser(result.Capabilities.Name, result.Capabilities.Version) {
		return s, ErrVersion
	}
	s.id, _, err = page(ctx, d, "")
	if err != nil {
		return s, err
	}
	// All required commands are tested before visiting the login page. Unknown
	// versions or missing screenshot/input/preload capabilities fail closed.
	for _, probe := range []struct {
		name string
		args map[string]any
	}{
		{"browsingContext.setViewport", map[string]any{"context": s.id, "viewport": map[string]int{"width": 1280, "height": 800}, "devicePixelRatio": 1}},
		{"storage.getCookies", map[string]any{"partition": map[string]string{"type": "context", "context": s.id}}},
		{"script.evaluate", map[string]any{"expression": "navigator.userAgent", "target": map[string]string{"context": s.id}, "awaitPromise": false}},
		{"input.performActions", map[string]any{"context": s.id, "actions": []any{}}},
		{"script.addPreloadScript", map[string]any{"functionDeclaration": observerScript, "contexts": []string{s.id}}},
	} {
		if _, err = d.Command(ctx, probe.name, probe.args); err != nil {
			return s, ErrVersion
		}
	}
	if _, err = s.Frame(ctx); err != nil {
		return s, ErrVersion
	}
	_, err = d.Command(ctx, "browsingContext.navigate", map[string]any{"context": s.id, "url": "https://business.gemini.google/", "wait": "none"})
	return s, err
}
func (s *Session) Close() error {
	s.once.Do(func() {
		if s.driver.Close() != nil {
			s.err = ErrCleanup
		}
	})
	return s.err
}
func (s *Session) Snapshot(ctx context.Context) (vault.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ready, err := snapshot(ctx, s.driver, s.id)
	if err != nil {
		return vault.Credential{}, err
	}
	if !ready {
		return vault.Credential{}, ErrMissing
	}
	c.ProxyURL = s.options.ProxyURL
	return c, nil
}
func (s *Session) State(ctx context.Context) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, address, err := page(ctx, s.driver, s.id)
	if err != nil {
		return View{}, err
	}
	config, index := metadata(address)
	missing := []string{}
	if config == "" {
		missing = append(missing, "config_id")
	}
	if index == "" {
		missing = append(missing, "csesidx")
	}
	c, ready, err := snapshot(ctx, s.driver, s.id)
	if err != nil {
		return View{}, err
	}
	if c.SecureCSES == "" {
		missing = append(missing, "session_cookie")
	}
	state := "waiting"
	if ready {
		state = "ready"
		missing = []string{}
	}
	return View{state, missing}, nil
}
func (s *Session) Frame(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.driver.Command(ctx, "browsingContext.captureScreenshot", map[string]any{"context": s.id, "origin": "viewport", "format": map[string]string{"type": "image/png"}})
	if err != nil || len(raw) > 6<<20 {
		return nil, ErrBrowser
	}
	var frame struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return nil, ErrBrowser
	}
	data, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil || len(data) > 4<<20 {
		return nil, ErrBrowser
	}
	c, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || c.Width > 2048 || c.Height > 2048 || c.Width < 1 || c.Height < 1 {
		return nil, ErrBrowser
	}
	return data, nil
}
func (s *Session) Input(ctx context.Context, input Input) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions := []any{}
	kind := "key"
	switch input.Type {
	case "click":
		if input.X < 0 || input.X >= 1280 || input.Y < 0 || input.Y >= 800 {
			return ErrBrowser
		}
		kind = "pointer"
		actions = []any{map[string]any{"type": "pointerMove", "x": input.X, "y": input.Y, "origin": "viewport"}, map[string]any{"type": "pointerDown", "button": 0}, map[string]any{"type": "pointerUp", "button": 0}}
	case "key", "text":
		text := input.Text
		if input.Type == "key" {
			text = map[string]string{"Enter": "\ue007", "Tab": "\ue004", "Backspace": "\ue003", "Escape": "\ue00c", "ArrowLeft": "\ue012", "ArrowUp": "\ue013", "ArrowRight": "\ue014", "ArrowDown": "\ue015"}[input.Key]
		}
		if text == "" || len(text) > 2048 {
			return ErrBrowser
		}
		for _, r := range text {
			actions = append(actions, map[string]any{"type": "keyDown", "value": string(r)}, map[string]any{"type": "keyUp", "value": string(r)})
		}
	default:
		return ErrBrowser
	}
	// WebDriver binds a source ID to its device type for the whole session.
	// Pointer and keyboard must not reuse one ID when the user switches devices.
	source := map[string]any{"type": kind, "id": "business-manual-" + kind, "actions": actions}
	if kind == "pointer" {
		source["parameters"] = map[string]string{"pointerType": "mouse"}
	}
	_, err := s.driver.Command(ctx, "input.performActions", map[string]any{"context": s.id, "actions": []any{source}})
	return err
}
func (s *Session) Observations(ctx context.Context) ([]Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, address, err := page(ctx, s.driver, s.id)
	if err != nil {
		return nil, err
	}
	if _, ok := businessURL(address); !ok {
		return nil, nil
	}
	raw, err := s.driver.Command(ctx, "script.evaluate", map[string]any{"expression": "JSON.stringify(window.__businessObserve ? window.__businessObserve() : [])", "target": map[string]string{"context": s.id}, "awaitPromise": false})
	if err != nil || len(raw) > 512<<10 {
		return nil, ErrBrowser
	}
	var result struct {
		Type   string `json:"type"`
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Type != "success" || result.Result.Type != "string" || len(result.Result.Value) > 256<<10 {
		return nil, ErrBrowser
	}
	var items []Observation
	if json.Unmarshal([]byte(result.Result.Value), &items) != nil || len(items) > 64 {
		return nil, ErrBrowser
	}
	return items, nil
}
