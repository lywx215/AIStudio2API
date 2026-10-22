package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConnection struct {
	responses                   []string
	writes                      []json.RawMessage
	readDeadline, writeDeadline time.Time
	readStarted                 chan struct{}
	closed                      chan struct{}
	readOnce, closeOnce         sync.Once
	closes                      atomic.Int32
}

func newFakeConnection(messages ...string) *fakeConnection {
	return &fakeConnection{responses: messages, readStarted: make(chan struct{}), closed: make(chan struct{})}
}
func (c *fakeConnection) SetWriteDeadline(deadline time.Time) error {
	c.writeDeadline = deadline
	return nil
}
func (c *fakeConnection) SetReadDeadline(deadline time.Time) error {
	c.readDeadline = deadline
	return nil
}
func (c *fakeConnection) WriteJSON(value any) error {
	c.writes = append(c.writes, rawJSON(value))
	return nil
}
func (c *fakeConnection) ReadJSON(value any) error {
	if len(c.responses) > 0 {
		message := c.responses[0]
		c.responses = c.responses[1:]
		return json.Unmarshal([]byte(message), value)
	}
	c.readOnce.Do(func() { close(c.readStarted) })
	<-c.closed
	return io.EOF
}
func (c *fakeConnection) Close() error {
	c.closeOnce.Do(func() { c.closes.Add(1); close(c.closed) })
	return nil
}

func fakeLiveDriver(t *testing.T, connection bidiConnection) *liveDriver {
	t.Helper()
	root := t.TempDir()
	profile, err := os.MkdirTemp(root, "collect-")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	return &liveDriver{connection: connection, done: done, profileRoot: root, profile: profile, stopTree: func() error { return nil }, removeProfile: os.RemoveAll}
}

func TestLauncherEndpointWriterWaitsForCompleteLine(t *testing.T) {
	w := &endpointWriter{ready: make(chan string, 1)}
	for _, chunk := range []string{"WebDriver BiDi listening on ws://127.0.0.1:12", "345/pa", "th\r"} {
		_, _ = w.Write([]byte(chunk))
		select {
		case <-w.ready:
			t.Fatal("partial endpoint was emitted")
		default:
		}
	}
	_, _ = w.Write([]byte("\n"))
	if got := <-w.ready; got != "ws://127.0.0.1:12345/path" {
		t.Fatalf("chunked endpoint was truncated: %s", got)
	}
	_, _ = w.Write([]byte(strings.Repeat("x", 8193) + " ws://127.0.0.1:1234\n"))
	select {
	case <-w.ready:
		t.Fatal("overlong line suffix was accepted")
	default:
	}
	_, _ = w.Write([]byte("ws://[::1]:4321\n"))
	if got := <-w.ready; got != "ws://[::1]:4321" {
		t.Fatal("writer did not recover at newline")
	}
}

func TestLauncherEndpointRejectsNonLoopbackAndInvalidPort(t *testing.T) {
	for _, endpoint := range []string{"wss://127.0.0.1:1234", "ws://example.com:1234", "ws://127.0.0.1.evil:1234", "ws://user@127.0.0.1:1234", "ws://127.0.0.1:0", "ws://127.0.0.1:65536", "ws://127.0.0.1", "ws://127.0.0.1:1234/?token=invalid", "ws://127.0.0.1:1234/#fragment"} {
		if _, err := sessionEndpoint(endpoint); err == nil {
			t.Errorf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if endpoint, err := sessionEndpoint("ws://127.0.0.1:1234/ignored-path"); err != nil || endpoint != "ws://127.0.0.1:1234/session" {
		t.Fatal("session endpoint was not normalized")
	}
}

func TestBiDiProtocolEventsAndMatchingResponse(t *testing.T) {
	c := newFakeConnection(`{"type":"event","method":"browsingContext.load","params":{}}`, `{"type":"success","id":1,"result":{"synthetic":true}}`)
	d := fakeLiveDriver(t, c)
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := d.Command(ctx, "session.new", map[string]any{"capabilities": map[string]any{}})
	if err != nil || string(result) != `{"synthetic":true}` {
		t.Fatalf("BiDi response failed: %v", err)
	}
	if len(c.writes) != 1 || !strings.Contains(string(c.writes[0]), `"id":1`) {
		t.Error("command correlation ID missing")
	}
	deadline, _ := ctx.Deadline()
	if c.readDeadline.After(deadline) || c.writeDeadline.After(deadline) {
		t.Error("command deadlines exceed caller deadline")
	}
}

func TestBiDiProtocolRejectsMismatchedAndErrorResponses(t *testing.T) {
	for _, message := range []string{`{"type":"success","id":2,"result":{}}`, `{"type":"error","id":1,"error":"synthetic secret"}`, `{"type":"success","id":1}`, `invalid-json`} {
		c := newFakeConnection(message)
		d := fakeLiveDriver(t, c)
		_, err := d.Command(context.Background(), "session.new", nil)
		if !errors.Is(err, ErrBrowser) || strings.Contains(err.Error(), "secret") {
			t.Errorf("invalid response was accepted or leaked detail: %v", err)
		}
		_ = d.Close()
	}
}

func TestDriverCloseInterruptsPendingReadBeforeTreeCleanup(t *testing.T) {
	c := newFakeConnection()
	d := fakeLiveDriver(t, c)
	legacy := filepath.Join(d.profileRoot, "old-aistudio-profile")
	if err := os.WriteFile(legacy, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Int32
	d.stopContext = func() bool { stopped.Add(1); return true }
	d.stopTree = func() error {
		select {
		case <-c.closed:
		default:
			t.Error("process termination ran before socket Close")
		}
		return nil
	}
	commandDone := make(chan error, 1)
	go func() { _, err := d.Command(context.Background(), "session.new", nil); commandDone <- err }()
	select {
	case <-c.readStarted:
	case <-time.After(time.Second):
		t.Fatal("command never reached read")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- d.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked behind pending command")
	}
	select {
	case err := <-commandDone:
		if !errors.Is(err, ErrBrowser) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("socket Close did not interrupt read")
	}
	if _, err := os.Stat(d.profile); !errors.Is(err, os.ErrNotExist) {
		t.Error("owned profile was not removed")
	}
	if data, err := os.ReadFile(legacy); err != nil || string(data) != "untouched" {
		t.Error("unrelated profile changed")
	}
	_ = d.Close()
	if stopped.Load() != 1 || c.closes.Load() != 1 {
		t.Error("Close/AfterFunc stop was not idempotent")
	}
}

func TestDriverCommandCancellationInterruptsRead(t *testing.T) {
	c := newFakeConnection()
	d := fakeLiveDriver(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := d.Command(ctx, "session.new", nil); finished <- err }()
	<-c.readStarted
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt command")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDriverCleanupFailurePreservesProfile(t *testing.T) {
	d := fakeLiveDriver(t, newFakeConnection())
	d.stopTree = func() error { return errors.New("tree still running") }
	if err := d.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatal("unconfirmed process cleanup accepted")
	}
	if _, err := os.Stat(d.profile); err != nil {
		t.Error("profile removed before tree termination was confirmed")
	}
	d = fakeLiveDriver(t, newFakeConnection())
	d.removeProfile = func(string) error { return errors.New("filesystem locked") }
	if err := d.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatal("profile cleanup error lost")
	}
}

func TestProfileCleanupRejectsOutsideOwnedRoot(t *testing.T) {
	d := fakeLiveDriver(t, newFakeConnection())
	called := false
	d.removeProfile = func(string) error { called = true; return nil }
	d.profile = filepath.Join(filepath.Dir(d.profileRoot), "collect-unowned")
	if err := d.Close(); !errors.Is(err, ErrCleanup) || called {
		t.Fatal("cleanup escaped owned profile root")
	}
}

func TestLauncherCanceledBeforeConnectionInstall(t *testing.T) {
	for range 100 {
		d := fakeLiveDriver(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		d.watchContext(ctx)
		cancel()
		c := newFakeConnection()
		if err := d.installConnection(ctx, c); !errors.Is(err, context.Canceled) {
			t.Fatal("late connection installed after cancellation")
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if c.closes.Load() != 1 {
			t.Fatal("rejected connection leaked")
		}
	}
	d := fakeLiveDriver(t, nil)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	c := newFakeConnection()
	if err := d.installConnection(context.Background(), c); !errors.Is(err, ErrBrowser) || c.closes.Load() != 1 {
		t.Fatal("closed driver accepted a connection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.watchContext(ctx)
	d.connectionMu.Lock()
	watcher := d.stopContext
	d.connectionMu.Unlock()
	if watcher != nil {
		t.Fatal("context watcher installed after Close")
	}
}

func TestLauncherRejectsInvalidOptionsWithoutStartingBrowser(t *testing.T) {
	// Invalid paths stop before exec.Start; this test never starts a browser.
	_, err := Launch(context.Background(), Options{ExecutablePath: "relative-firefox.exe", Directory: t.TempDir()})
	if !errors.Is(err, ErrVersion) && !errors.Is(err, ErrLocalDisplay) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Launch(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled launch did work")
	}
}

func TestProfileEnvironmentSeparatesLegacyAndTemporaryState(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "collect-synthetic")
	environment := browserEnvironment([]string{"PATH=retained", "CAMOU_CONFIG_1=old-fingerprint", "camou_config=old-fingerprint", "MOZ_HEADLESS=1", "MOZ_HEADLESS_WIDTH=1920", "MOZ_PROFILE=old-profile", "MOZ_LOG_FILE=old-log", "MOZ_CRASHREPORTER_DATA_DIRECTORY=old-crashes", "TMP=old-tmp", "TEMP=old-temp", "TMPDIR=old-tmpdir"}, profile)
	if len(environment) != 4 || environment[0] != "PATH=retained" {
		t.Fatal("legacy environment override was inherited")
	}
	for _, item := range environment[1:] {
		_, value, _ := strings.Cut(item, "=")
		if value != filepath.Join(profile, "tmp") {
			t.Fatal("browser temporary state escaped its profile")
		}
	}
}
