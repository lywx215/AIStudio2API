// Package browser owns a disposable Business login browser. It never invokes
// AI Studio's Worker/Login or reads its profiles, cookies or fingerprint files.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

var (
	ErrBrowser      = errors.New("Business browser operation failed")
	ErrMissing      = errors.New("Business session fields were not observed before timeout")
	ErrVersion      = errors.New("Business collection requires a tested browser release and BiDi capabilities")
	ErrCleanup      = errors.New("Business browser cleanup failed; its credential snapshot was discarded")
	ErrLocalDisplay = errors.New("Business visible collection requires a local desktop; remote/headless collection is not implemented")
)

type Options struct {
	ExecutablePath string
	Headless       bool
	ProxyURL       string
	UserAgent      string
	// Directory is an explicit, absolute Business-only disposable profile root.
	Directory string
}

// Driver implements the small WebDriver BiDi command surface used by Collect.
// Close must interrupt pending commands, terminate only this driver's process
// tree, and report an error unless that tree and its profile were cleaned up.
type Driver interface {
	Command(context.Context, string, map[string]any) (json.RawMessage, error)
	Close() error
}

type Launcher func(context.Context, Options) (Driver, error)

var configToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var indexToken = regexp.MustCompile(`^[0-9]{1,128}$`)

// Collect observes a login performed by the user. No prompt is typed and no
// generation, token probe, AI Studio page or Drive authorization is requested.
// The returned snapshot is usable only after the disposable browser is closed.
// Missing URL fields remain pending: BiDi request events are not assumed to
// contain request bodies, and this collector never guesses model/config IDs.
func Collect(ctx context.Context, options Options, launch Launcher) (credential vault.Credential, err error) {
	if ctx.Err() != nil {
		return vault.Credential{}, ctx.Err()
	}
	if launch == nil {
		launch = Launch
	}
	driver, err := launch(ctx, options)
	if err != nil {
		return vault.Credential{}, err
	}
	if driver == nil {
		return vault.Credential{}, ErrBrowser
	}
	defer func() {
		if driver.Close() != nil {
			credential = vault.Credential{}
			err = errors.Join(err, ErrCleanup)
		}
		if ctx.Err() != nil {
			credential = vault.Credential{}
			err = errors.Join(err, ctx.Err())
		}
	}()
	raw, err := driver.Command(ctx, "session.new", map[string]any{"capabilities": map[string]any{}})
	if err != nil {
		return vault.Credential{}, commandError(ctx)
	}
	var session struct {
		Capabilities struct {
			Name    string `json:"browserName"`
			Version string `json:"browserVersion"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &session) != nil || !supportedBrowser(session.Capabilities.Name, session.Capabilities.Version) {
		return vault.Credential{}, ErrVersion
	}
	id, _, err := page(ctx, driver, "")
	if err != nil {
		return vault.Credential{}, err
	}
	_, err = driver.Command(ctx, "browsingContext.navigate", map[string]any{"context": id, "url": "https://business.gemini.google/", "wait": "none"})
	if err != nil {
		return vault.Credential{}, commandError(ctx)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		credential, ready, snapshotErr := snapshot(ctx, driver, id)
		if snapshotErr != nil {
			return vault.Credential{}, snapshotErr
		}
		if ready {
			return credential, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return vault.Credential{}, errors.Join(ErrMissing, ctx.Err())
			}
			return vault.Credential{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func commandError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrBrowser
}

func snapshot(ctx context.Context, driver Driver, id string) (vault.Credential, bool, error) {
	_, address, err := page(ctx, driver, id)
	if err != nil {
		return vault.Credential{}, false, err
	}
	configID, index := metadata(address)
	if configID == "" || index == "" {
		return vault.Credential{}, false, nil
	}
	readCookies := func() (vault.Credential, bool, error) {
		raw, err := driver.Command(ctx, "storage.getCookies", map[string]any{"partition": map[string]string{"type": "context", "context": id}})
		if err != nil {
			return vault.Credential{}, false, commandError(ctx)
		}
		credential, ok := cookies(raw, address)
		return credential, ok, nil
	}
	credential, ok, err := readCookies()
	if err != nil || !ok {
		return vault.Credential{}, false, err
	}
	raw, err := driver.Command(ctx, "script.evaluate", map[string]any{"expression": "navigator.userAgent", "target": map[string]string{"context": id}, "awaitPromise": false})
	if err != nil {
		return vault.Credential{}, false, commandError(ctx)
	}
	var evaluation struct {
		Type   string `json:"type"`
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &evaluation) != nil || evaluation.Type != "success" || evaluation.Result.Type != "string" || evaluation.Result.Value == "" {
		return vault.Credential{}, false, ErrBrowser
	}
	// Re-read both location and cookies. An account switch or cookie refresh
	// during collection must not mix values from two different snapshots.
	_, after, err := page(ctx, driver, id)
	if err != nil {
		return vault.Credential{}, false, err
	}
	if after != address {
		return vault.Credential{}, false, nil
	}
	confirmed, ok, err := readCookies()
	if err != nil || !ok || confirmed != credential {
		return vault.Credential{}, false, err
	}
	credential.ConfigID, credential.CSESIndex, credential.UserAgent = configID, index, evaluation.Result.Value
	if credential.Validate() != nil {
		return vault.Credential{}, false, ErrBrowser
	}
	return credential, true, nil
}

func page(ctx context.Context, driver Driver, id string) (string, string, error) {
	params := map[string]any{"maxDepth": 0}
	if id != "" {
		params["root"] = id
	}
	raw, err := driver.Command(ctx, "browsingContext.getTree", params)
	if err != nil {
		return "", "", commandError(ctx)
	}
	var tree struct {
		Contexts []struct {
			ID  string `json:"context"`
			URL string `json:"url"`
		} `json:"contexts"`
	}
	if json.Unmarshal(raw, &tree) != nil || len(tree.Contexts) != 1 || tree.Contexts[0].ID == "" || (id != "" && tree.Contexts[0].ID != id) {
		return "", "", ErrBrowser
	}
	return tree.Contexts[0].ID, tree.Contexts[0].URL, nil
}

func businessURL(address string) (*url.URL, bool) {
	u, err := url.Parse(address)
	return u, err == nil && u.Scheme == "https" && u.Host == "business.gemini.google" && u.User == nil && u.Opaque == ""
}

// These explicit named URL fields are observed by the reference client. The
// source is the current Business context URL, not HTML numbers or model labels.
func metadata(address string) (string, string) {
	u, ok := businessURL(address)
	if !ok {
		return "", ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	index := query["csesidx"]
	if err != nil || len(index) != 1 || !indexToken.MatchString(index[0]) {
		return "", ""
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	configID := ""
	for i, segment := range segments {
		if segment == "cid" {
			if configID != "" || i+1 >= len(segments) || len(segments[i+1]) > 1024 || !configToken.MatchString(segments[i+1]) {
				return "", ""
			}
			configID = segments[i+1]
		}
	}
	if configID == "" {
		return "", ""
	}
	return configID, index[0]
}

func cookies(raw json.RawMessage, address string) (vault.Credential, bool) {
	u, ok := businessURL(address)
	if !ok {
		return vault.Credential{}, false
	}
	var data struct {
		Cookies []struct {
			Name   string   `json:"name"`
			Domain string   `json:"domain"`
			Path   string   `json:"path"`
			Secure bool     `json:"secure"`
			Expiry *float64 `json:"expiry"`
			Value  struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"value"`
		} `json:"cookies"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return vault.Credential{}, false
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	values := map[string]string{}
	for _, c := range data.Cookies {
		if c.Name != "__Secure-C_SES" && c.Name != "__Host-C_OSES" {
			continue
		}
		if strings.TrimPrefix(c.Domain, ".") != "business.gemini.google" || !c.Secure || c.Value.Type != "string" {
			continue
		}
		if c.Expiry != nil && *c.Expiry <= float64(time.Now().UnixNano())/float64(time.Second) {
			continue
		}
		if !cookiePathMatches(path, c.Path) {
			continue
		}
		if c.Name == "__Host-C_OSES" && (c.Domain != "business.gemini.google" || c.Path != "/") {
			continue
		}
		if previous, exists := values[c.Name]; exists && previous != c.Value.Value {
			return vault.Credential{}, false
		}
		values[c.Name] = c.Value.Value
	}
	return vault.Credential{SecureCSES: values["__Secure-C_SES"], HostCOSES: values["__Host-C_OSES"]}, values["__Secure-C_SES"] != ""
}

func cookiePathMatches(request, cookie string) bool {
	if cookie == "" || cookie[0] != '/' {
		return false
	}
	return request == cookie || (strings.HasPrefix(request, cookie) && (strings.HasSuffix(cookie, "/") || (len(request) > len(cookie) && request[len(cookie)] == '/')))
}
