package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
	"github.com/gorilla/websocket"
)

type endpointWriter struct {
	mu      sync.Mutex
	tail    []byte
	discard bool
	ready   chan string
}

var endpointPattern = regexp.MustCompile(`(?:^|[ \t])(ws://(?:127\.0\.0\.1|localhost|\[::1\]):[0-9]+[^\s]*)[ \t]*$`)

func (w *endpointWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Emit only after the complete line arrives. Split writes must not turn
	// a partial port/path into a ready endpoint. Overlong lines are discarded.
	for _, b := range p {
		if b == '\n' {
			if !w.discard {
				line := strings.TrimSuffix(string(w.tail), "\r")
				if found := endpointPattern.FindStringSubmatch(line); len(found) == 2 {
					if _, err := sessionEndpoint(found[1]); err == nil {
						select {
						case w.ready <- found[1]:
						default:
						}
					}
				}
			}
			w.tail, w.discard = w.tail[:0], false
		} else if !w.discard {
			if len(w.tail) == 8192 {
				w.tail, w.discard = w.tail[:0], true
			} else {
				w.tail = append(w.tail, b)
			}
		}
	}
	return len(p), nil
}

func sessionEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ws" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrBrowser
	}
	if host := u.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return "", ErrBrowser
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", ErrBrowser
	}
	u.Path, u.RawPath = "/session", ""
	return u.String(), nil
}

type bidiConnection interface {
	SetWriteDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	WriteJSON(any) error
	ReadJSON(any) error
	Close() error
}

type liveDriver struct {
	commandMu     sync.Mutex // One BiDi command reader/writer at a time.
	connectionMu  sync.Mutex // Never held while performing socket I/O.
	connection    bidiConnection
	closed        bool
	stopContext   func() bool
	id            uint64
	done          <-chan struct{}
	profileRoot   string
	profile       string
	stopTree      func() error
	removeProfile func(string) error
	once          sync.Once
	closeErr      error
}

// Launch is used only by an explicit, authenticated collection request. The
// browser is visible on the server desktop. Remote/headless collection is not
// implemented. Binary reuse is read-only, with a fresh dedicated profile.
func Launch(ctx context.Context, options Options) (result Driver, resultErr error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		options.Headless = true
	}
	if vault.ValidateProxy(options.ProxyURL) != nil || len(options.UserAgent) > 1024 || strings.ContainsAny(options.UserAgent, "\r\n\x00") {
		return nil, ErrBrowser
	}
	base := strings.ToLower(filepath.Base(options.ExecutablePath))
	if !filepath.IsAbs(options.ExecutablePath) || (base != "camoufox.exe" && base != "firefox.exe" && base != "camoufox" && base != "firefox") {
		return nil, ErrVersion
	}
	info, err := os.Stat(options.ExecutablePath)
	if err != nil || !info.Mode().IsRegular() || !filepath.IsAbs(options.Directory) {
		return nil, ErrBrowser
	}
	root := filepath.Clean(options.Directory)
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, ErrBrowser
	}
	profile, err := os.MkdirTemp(root, "collect-")
	if err != nil {
		return nil, ErrBrowser
	}
	driver := &liveDriver{profileRoot: root, profile: profile, removeProfile: os.RemoveAll}
	// Before Start, no process owns the fresh profile. After Start, only Close
	// may remove it, and only after independently confirming tree termination.
	started := false
	defer func() {
		if result == nil {
			var cleanupErr error
			if started {
				cleanupErr = driver.Close()
			} else {
				cleanupErr = driver.cleanupProfile()
			}
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, ErrCleanup)
			}
		}
	}()
	prefs := "user_pref(\"remote.active-protocols\", 1);\nuser_pref(\"browser.shell.checkDefaultBrowser\", false);\nuser_pref(\"browser.startup.page\", 0);\nuser_pref(\"browser.sessionstore.resume_from_crash\", false);\n"
	prefs += "user_pref(\"network.proxy.type\", 0);\nuser_pref(\"media.peerconnection.enabled\", false);\n"
	if options.UserAgent != "" {
		prefs += "user_pref(\"general.useragent.override\", " + strconv.Quote(options.UserAgent) + ");\n"
	}
	if options.ProxyURL != "" {
		u, _ := url.Parse(options.ProxyURL)
		port, _ := strconv.Atoi(u.Port())
		host := strconv.Quote(u.Hostname())
		prefs += "user_pref(\"network.proxy.type\", 1);\nuser_pref(\"network.proxy.no_proxies_on\", \"\");\n"
		if u.Scheme == "socks5" {
			prefs += "user_pref(\"network.proxy.socks\", " + host + ");\nuser_pref(\"network.proxy.socks_port\", " + strconv.Itoa(port) + ");\nuser_pref(\"network.proxy.socks_version\", 5);\nuser_pref(\"network.proxy.socks_remote_dns\", true);\n"
		} else {
			for _, scheme := range []string{"http", "ssl"} {
				prefs += "user_pref(\"network.proxy." + scheme + "\", " + host + ");\nuser_pref(\"network.proxy." + scheme + "_port\", " + strconv.Itoa(port) + ");\n"
			}
		}
	}
	if err := os.Mkdir(filepath.Join(profile, "tmp"), 0700); err != nil {
		return nil, ErrBrowser
	}
	if err := os.WriteFile(filepath.Join(profile, "user.js"), []byte(prefs), 0600); err != nil {
		return nil, ErrBrowser
	}
	args := []string{"--remote-debugging-port=0"}
	if options.Headless {
		args = append(args, "-no-remote", "-headless")
	} else {
		args = append(args, "-no-remote", "-wait-for-browser")
	}
	args = append(args, "-profile", profile)
	command := exec.Command(options.ExecutablePath, args...)
	command.Dir = profile
	command.Env = browserEnvironment(os.Environ(), profile)
	output := &endpointWriter{ready: make(chan string, 1)}
	command.Stdout, command.Stderr = output, output
	configureProcess(command)
	if options.Headless {
		configureHeadless(command)
	}
	if err := command.Start(); err != nil {
		return nil, ErrBrowser
	}
	started = true
	done := make(chan struct{})
	driver.done = done
	go func() { _ = command.Wait(); close(done) }()
	// Until bindProcess succeeds, Windows has not resumed this process. Keep
	// a bounded cleanup path even if binding fails or panics.
	driver.stopTree = func() error {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return ErrCleanup
		}
		return nil
	}
	stopTree, err := bindProcess(command)
	if err != nil {
		// On Windows the process remains suspended until its private job is
		// bound, so this failure cannot have spawned browser children.
		return nil, ErrBrowser
	}
	driver.stopTree = stopTree
	driver.watchContext(ctx)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var endpoint string
	select {
	case endpoint = <-output.ready:
	case <-driver.done:
		return nil, ErrBrowser
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrBrowser
	}
	address, err := sessionEndpoint(endpoint)
	if err != nil {
		return nil, ErrBrowser
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	connection, response, err := dialer.DialContext(ctx, address, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, commandError(ctx)
	}
	connection.SetReadLimit(6 << 20) // Bounded PNG screenshot plus BiDi envelope.
	if err = driver.installConnection(ctx, connection); err != nil {
		return nil, err
	}
	return driver, nil
}

// Fingerprint configuration and headless/profile overrides from the old
// browser environment must not enter this dedicated visible collection.
func browserEnvironment(inherited []string, profile string) []string {
	result := make([]string, 0, len(inherited)+4)
	for _, item := range inherited {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "CAMOU_CONFIG") || strings.HasPrefix(key, "MOZ_HEADLESS") || key == "MOZ_PROFILE" || key == "MOZ_LOG_FILE" || key == "MOZ_CRASHREPORTER_DATA_DIRECTORY" || key == "TMP" || key == "TEMP" || key == "TMPDIR" {
			continue
		}
		result = append(result, item)
	}
	temporary := filepath.Join(profile, "tmp")
	return append(result, "TMP="+temporary, "TEMP="+temporary, "TMPDIR="+temporary)
}

func (d *liveDriver) watchContext(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = d.Close() })
	d.connectionMu.Lock()
	if d.closed {
		d.connectionMu.Unlock()
		stop()
		return
	}
	d.stopContext = stop
	d.connectionMu.Unlock()
}

func (d *liveDriver) installConnection(ctx context.Context, connection bidiConnection) error {
	d.connectionMu.Lock()
	if d.closed || ctx.Err() != nil || d.connection != nil {
		d.connectionMu.Unlock()
		_ = connection.Close()
		return commandError(ctx)
	}
	d.connection = connection
	d.connectionMu.Unlock()
	return nil
}

func (d *liveDriver) cleanupProfile() error {
	path, root := filepath.Clean(d.profile), filepath.Clean(d.profileRoot)
	if !filepath.IsAbs(root) || filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), "collect-") || d.removeProfile == nil {
		return ErrCleanup
	}
	for attempt := 0; attempt < 20; attempt++ {
		err := d.removeProfile(path)
		if err == nil {
			return nil
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			return ErrCleanup
		}
		// Terminated Windows children can release SQLite handles shortly after
		// their job reaches zero processes. Retry only this verified owned path.
		time.Sleep(100 * time.Millisecond)
	}
	return ErrCleanup
}

func (d *liveDriver) Close() error {
	d.once.Do(func() {
		d.connectionMu.Lock()
		d.closed = true
		connection, stop := d.connection, d.stopContext
		d.connection, d.stopContext = nil, nil
		d.connectionMu.Unlock()
		if stop != nil {
			stop()
		}
		// Do not take commandMu: a pending ReadJSON owns it. Closing the
		// socket first interrupts that read before tree termination is awaited.
		if connection != nil {
			_ = connection.Close()
		}
		if d.stopTree == nil || d.stopTree() != nil {
			d.closeErr = ErrCleanup
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-d.done:
		case <-timer.C:
			d.closeErr = ErrCleanup
		}
		if d.closeErr == nil {
			d.closeErr = d.cleanupProfile()
		}
	})
	return d.closeErr
}

func (d *liveDriver) Command(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() { _ = d.Close() })
	defer stop()
	d.commandMu.Lock()
	defer d.commandMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	d.connectionMu.Lock()
	connection := d.connection
	d.connectionMu.Unlock()
	if connection == nil {
		return nil, ErrBrowser
	}
	deadline := time.Now().Add(10 * time.Second)
	if method == "session.new" {
		deadline = time.Now().Add(30 * time.Second)
	}
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	d.id++
	if connection.SetWriteDeadline(deadline) != nil || connection.SetReadDeadline(deadline) != nil || connection.WriteJSON(map[string]any{"id": d.id, "method": method, "params": params}) != nil {
		return nil, commandError(ctx)
	}
	for {
		var response struct {
			ID     uint64          `json:"id"`
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if readErr := connection.ReadJSON(&response); readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: %s response unavailable", ErrBrowser, method)
		}
		if response.Type == "event" && response.ID == 0 {
			continue
		}
		if response.ID != d.id || response.Type != "success" || len(response.Result) == 0 {
			if response.ID == d.id && response.Type == "error" {
				for _, code := range []string{"unknown command", "unsupported operation", "invalid argument", "session not created", "no such frame", "unknown error"} {
					if response.Error == code {
						return nil, fmt.Errorf("%w: %s (%s)", ErrBrowser, method, code)
					}
				}
			}
			return nil, ErrBrowser
		}
		return response.Result, nil
	}
}

var _ io.Writer = (*endpointWriter)(nil)
