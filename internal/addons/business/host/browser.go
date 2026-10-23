package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/browser"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/observe"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type browserTask struct {
	mu                                 sync.Mutex
	id, owner, accountID, label, state string
	revision                           uint64
	ctx                                context.Context
	cancel                             context.CancelFunc
	expires, touched                   time.Time
	session                            *browser.Session
	missing                            []string
	pending                            []observe.Observation
	observedCredential                 vault.Credential
}
type taskView struct {
	ID      string    `json:"task_id"`
	State   string    `json:"state"`
	Missing []string  `json:"missing_fields"`
	Expires time.Time `json:"expires_at"`
	Count   int       `json:"observations_count"`
}

func (t *browserTask) view() taskView {
	return taskView{t.id, t.state, append([]string{}, t.missing...), t.expires, len(t.pending)}
}
func decodeSmall(r *http.Request, value any) bool {
	d := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	d.DisallowUnknownFields()
	return d.Decode(value) == nil && d.Decode(new(any)) == io.EOF
}

func (h *Host) handleBrowser(w http.ResponseWriter, r *http.Request) {
	console, ok := contract.ConsoleFrom(r.Context())
	if !ok {
		contract.Error(w, 401, "UNAUTHENTICATED", "A console session is required")
		return
	}
	if r.URL.Path == "/api/addons/business/browser-info" && r.Method == "GET" {
		path := os.Getenv("CAMOUFOX_PATH")
		if path != "" {
			path, _ = filepath.Abs(path)
		}
		writeJSON(w, map[string]any{"executable_path": path, "tested_versions": browser.TestedVersions, "capabilities": []string{"screen", "click", "key", "text", "passive_model_observation"}})
		return
	}
	if r.URL.Path == "/api/addons/business/browser" && r.Method == "POST" {
		h.startBrowser(w, r, console)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/addons/business/browser/"), "/")
	if len(parts) > 2 {
		contract.Error(w, 404, "NOT_FOUND", "Unknown browser operation")
		return
	}
	h.remoteMu.Lock()
	t := h.remote
	if t == nil || t.id != parts[0] {
		t = h.remoteLast
	}
	h.remoteMu.Unlock()
	if t == nil || t.id != parts[0] || t.owner != console.ID {
		contract.Error(w, 404, "NOT_FOUND", "Browser task is unavailable")
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx.Err() != nil {
		if len(parts) == 1 && (r.Method == "GET" || r.Method == "DELETE") {
			writeJSON(w, t.view())
			return
		}
		contract.Error(w, 404, "NOT_FOUND", "Browser task has ended")
		return
	}
	t.touched = time.Now()
	if len(parts) == 1 && r.Method == "DELETE" {
		t.state = "cancelled"
		t.cancel()
		writeJSON(w, t.view())
		return
	}
	if t.session == nil {
		if len(parts) == 1 && r.Method == "GET" {
			writeJSON(w, t.view())
			return
		}
		contract.Error(w, 409, "ABORTED", "Browser is still starting")
		return
	}
	// A cancelled polling/input request interrupts the browser and cannot leave
	// a live orphan. The owner session and host also bound every operation.
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	defer cancel()
	defer stop()
	if len(parts) == 1 && r.Method == "GET" {
		view, err := t.session.State(ctx)
		if err != nil {
			t.state = "failed"
			t.cancel()
			contract.Error(w, 503, "UNAVAILABLE", "Browser state is unavailable")
			return
		}
		t.state, t.missing = view.State, view.MissingFields
		h.drainObservations(ctx, t)
		writeJSON(w, t.view())
		return
	}
	if len(parts) != 2 {
		contract.Error(w, 405, "INVALID_ARGUMENT", "Method not allowed")
		return
	}
	switch {
	case parts[1] == "frame" && r.Method == "GET":
		data, err := t.session.Frame(ctx)
		if err != nil {
			contract.Error(w, 503, "UNAVAILABLE", "Browser frame is unavailable")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, _ = w.Write(data)
	case parts[1] == "input" && r.Method == "POST":
		var input browser.Input
		if !decodeSmall(r, &input) {
			contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid browser input")
			return
		}
		if err := t.session.Input(ctx, input); err != nil {
			contract.Error(w, 400, "INVALID_ARGUMENT", "Browser input was rejected")
			return
		}
		writeJSON(w, t.view())
	case parts[1] == "save" && r.Method == "POST":
		h.saveBrowser(w, r, ctx, t)
	default:
		contract.Error(w, 405, "INVALID_ARGUMENT", "Unknown browser operation")
	}
}

func (h *Host) startBrowser(w http.ResponseWriter, r *http.Request, console contract.Console) {
	var input struct {
		Revision       uint64  `json:"revision"`
		ID             string  `json:"id"`
		Label          string  `json:"label"`
		ExecutablePath string  `json:"executable_path"`
		Headless       bool    `json:"headless"`
		ProxyURL       *string `json:"proxy_url"`
	}
	if !decodeSmall(r, &input) || strings.TrimSpace(input.Label) == "" || len(input.Label) > 160 || !filepath.IsAbs(input.ExecutablePath) {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Account revision, label and absolute browser path are required")
		return
	}
	snapshot, err := h.accounts.Load()
	if err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Credential vault is unavailable")
		return
	}
	if snapshot.Revision != input.Revision {
		contract.Error(w, 409, "ABORTED", "Account revision changed")
		return
	}
	options := browser.Options{ExecutablePath: input.ExecutablePath, Headless: input.Headless}
	if input.ID != "" {
		index := slices.IndexFunc(snapshot.Accounts, func(a vault.Account) bool { return a.ID == input.ID })
		if index < 0 {
			contract.Error(w, 404, "NOT_FOUND", "Account is unavailable")
			return
		}
		c := snapshot.Accounts[index].Credential
		options.UserAgent, options.ProxyURL = c.UserAgent, c.ProxyURL
	}
	if input.ProxyURL != nil {
		options.ProxyURL = *input.ProxyURL
	}
	if vault.ValidateProxy(options.ProxyURL) != nil {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Proxy must be http or socks5 with explicit port and no authentication")
		return
	}
	options.Directory, err = filepath.Abs(filepath.Join(filepath.Dir(h.path), "browser"))
	if err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Browser storage is unavailable")
		return
	}
	h.remoteMu.Lock()
	defer h.remoteMu.Unlock()
	if h.remote != nil || h.remoteCleanup != nil || h.legacyCollect {
		contract.Error(w, 409, "ABORTED", "Another browser task is active or cleanup requires attention")
		return
	}
	h.operationMu.Lock()
	if h.closed.Load() {
		h.operationMu.Unlock()
		contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
		return
	}
	h.operations.Add(1)
	h.operationMu.Unlock()
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Minute)
	stopOwner := context.AfterFunc(console.Lifetime, cancel)
	_, lease := h.budget.Begin(ctx)
	if _, err = lease.Reserve(16 << 20); err != nil {
		lease.Close()
		cancel()
		stopOwner()
		h.operations.Done()
		contract.Error(w, 429, "RESOURCE_EXHAUSTED", "Browser data budget is unavailable")
		return
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	expires, _ := ctx.Deadline()
	t := &browserTask{id: hex.EncodeToString(id[:]), owner: console.ID, accountID: input.ID, label: strings.TrimSpace(input.Label), revision: input.Revision, ctx: ctx, cancel: cancel, expires: expires, touched: time.Now(), state: "starting", missing: []string{}}
	h.remote = t
	initial := t.view()
	go h.runBrowser(t, options, lease, stopOwner)
	writeJSON(w, initial)
}

func (h *Host) runBrowser(t *browserTask, options browser.Options, lease *resources.Lease, stopOwner func() bool) {
	defer h.operations.Done()
	defer lease.Close()
	defer stopOwner()
	defer t.cancel()
	session, err := browser.Open(t.ctx, options, h.launcher)
	t.mu.Lock()
	t.session = session
	if err != nil {
		t.state = "failed"
	} else {
		t.state = "waiting"
	}
	t.mu.Unlock()
	if err == nil {
		ticker := time.NewTicker(5 * time.Second)
		for t.ctx.Err() == nil {
			select {
			case <-t.ctx.Done():
			case <-ticker.C:
				t.mu.Lock()
				idle := time.Since(t.touched) > time.Minute
				t.mu.Unlock()
				if idle {
					t.cancel()
				}
			}
		}
		ticker.Stop()
		err = session.Close()
	}
	if errors.Is(err, browser.ErrCleanup) {
		h.remoteMu.Lock()
		h.remoteCleanup = browser.ErrCleanup
		h.remoteMu.Unlock()
	}
	t.mu.Lock()
	if errors.Is(err, browser.ErrCleanup) {
		t.state = "cleanup_failed"
	} else if t.state != "saved" && t.state != "failed" {
		if errors.Is(t.ctx.Err(), context.DeadlineExceeded) {
			t.state = "expired"
		} else {
			t.state = "cancelled"
		}
	}
	t.observedCredential = vault.Credential{}
	t.pending = nil
	t.session = nil
	t.mu.Unlock()
	h.remoteMu.Lock()
	if h.remote == t {
		h.remote = nil
		h.remoteLast = t
	}
	h.remoteMu.Unlock()
}

func (h *Host) drainObservations(ctx context.Context, t *browserTask) {
	credential, err := t.session.Snapshot(ctx)
	if err != nil {
		return
	}
	items, err := t.session.Observations(ctx)
	if err != nil {
		return
	}
	after, err := t.session.Snapshot(ctx)
	if err != nil || after != credential {
		t.pending = nil
		t.observedCredential = vault.Credential{}
		return
	}
	if t.observedCredential != credential {
		t.pending = nil
		t.observedCredential = credential
	}
	scope := observe.Scope{AccountID: "pending-" + t.id, CredentialEpoch: 1, ConfigID: credential.ConfigID}
	for _, item := range items {
		// Bind to the page at request dispatch, not to a later SPA account/config.
		if item.ConfigID != credential.ConfigID || item.CSESIndex != credential.CSESIndex {
			continue
		}
		parsed, err := observe.Parse(scope, observe.Source{Kind: item.Source, URL: item.URL}, item.Payload, time.Now())
		if err == nil && len(t.pending)+len(parsed) <= 128 {
			t.pending = append(t.pending, parsed...)
		}
	}
}

func (h *Host) saveBrowser(w http.ResponseWriter, r *http.Request, ctx context.Context, t *browserTask) {
	h.drainObservations(ctx, t)
	credential, err := t.session.Snapshot(ctx)
	if err != nil {
		contract.Error(w, 409, "FAILED_PRECONDITION", "Required account fields have not been observed")
		return
	}
	t.state = "saving"
	if err = t.session.Close(); err != nil {
		t.cancel()
		h.remoteMu.Lock()
		h.remoteCleanup = browser.ErrCleanup
		h.remoteMu.Unlock()
		contract.Error(w, 503, "UNAVAILABLE", "Browser cleanup failed; credentials were not saved")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil || t.ctx.Err() != nil || h.closed.Load() {
		t.cancel()
		contract.Error(w, 499, "CANCELLED", "Collection cancelled before saving")
		return
	}
	var saved vault.Account
	result, err := h.accounts.Update(t.revision, func(s *vault.Snapshot) error {
		if ctx.Err() != nil || t.ctx.Err() != nil {
			return context.Canceled
		}
		index := slices.IndexFunc(s.Accounts, func(a vault.Account) bool { return a.ID == t.accountID })
		if t.accountID != "" && index < 0 {
			return vault.ErrConflict
		}
		saved = vault.Account{ID: t.accountID, Label: t.label, Epoch: 1, Credential: credential}
		if index < 0 {
			saved.ID = vault.NewID()
			s.Accounts = append(s.Accounts, saved)
		} else {
			saved.Epoch = s.Accounts[index].Epoch + 1
			s.Accounts[index] = saved
		}
		return nil
	})
	t.cancel()
	if err != nil {
		code := 503
		if errors.Is(err, vault.ErrConflict) {
			code = 409
		}
		contract.Error(w, code, "ABORTED", "Accounts changed or collection could not be saved; refresh before retrying")
		return
	}
	h.reconcileAccountsLocked(result)
	if t.observedCredential == credential && len(t.pending) > 0 {
		_, _ = h.observations.Record(observe.Scope{AccountID: saved.ID, CredentialEpoch: saved.Epoch, ConfigID: credential.ConfigID}, t.pending)
	}
	t.state = "saved"
	writeJSON(w, accountResponse(result))
}

func (h *Host) handleObservations(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	accounts, err := h.accounts.Load()
	if err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Credential vault is unavailable")
		return
	}
	scopes := []observe.Scope{}
	for _, a := range accounts.Accounts {
		scopes = append(scopes, observe.Scope{AccountID: a.ID, CredentialEpoch: a.Epoch, ConfigID: a.Credential.ConfigID})
	}
	if _, err = h.observations.ReconcileScopes(scopes); err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Observation catalog is unavailable")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/addons/business/observations" {
		s, err := h.observations.List()
		if err != nil {
			contract.Error(w, 503, "UNAVAILABLE", "Observation catalog is unavailable")
			return
		}
		writeJSON(w, s)
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/addons/business/observations/confirm" {
		var input struct {
			Revision uint64 `json:"revision"`
			ID       string `json:"id"`
		}
		if !decodeSmall(r, &input) {
			contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid observation confirmation")
			return
		}
		s, err := h.observations.List()
		if err != nil {
			contract.Error(w, 503, "UNAVAILABLE", "Observation catalog is unavailable")
			return
		}
		for _, entry := range s.Entries {
			if entry.ID == input.ID {
				updated, err := h.observations.Confirm(input.Revision, input.ID, entry.Scope)
				if err != nil {
					contract.Error(w, 409, "ABORTED", "Observation expired, conflicted or changed")
					return
				}
				writeJSON(w, updated)
				return
			}
		}
	}
	contract.Error(w, 404, "NOT_FOUND", "Unknown observation operation")
}
