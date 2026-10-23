package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/browser"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

// handleCollect runs only inside the original consoleAuth wrapper. Its open
// request owns the visible browser; cancellation/logout closes that browser.
func (h *Host) handleCollect(w http.ResponseWriter, r *http.Request) {
	h.remoteMu.Lock()
	if h.remote != nil || h.remoteCleanup != nil || h.legacyCollect {
		h.remoteMu.Unlock()
		contract.Error(w, 409, "ABORTED", "Another browser task is active or cleanup requires attention")
		return
	}
	h.legacyCollect = true
	h.remoteMu.Unlock()
	defer func() { h.remoteMu.Lock(); h.legacyCollect = false; h.remoteMu.Unlock() }()
	if r.Method != http.MethodPost {
		contract.Error(w, 405, "INVALID_ARGUMENT", "Method not allowed")
		return
	}
	var input struct {
		Revision       uint64 `json:"revision"`
		ID             string `json:"id"`
		Label          string `json:"label"`
		ExecutablePath string `json:"executable_path"`
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || strings.TrimSpace(input.Label) == "" || len(input.Label) > 160 || !filepath.IsAbs(input.ExecutablePath) {
		contract.Error(w, 400, "INVALID_ARGUMENT", "A label, account revision and absolute server browser path are required")
		return
	}
	h.mu.RLock()
	closed, invalid := h.closed.Load(), h.problem != ""
	h.mu.RUnlock()
	if closed || invalid {
		contract.Error(w, 503, "UNAVAILABLE", "Business is unavailable")
		return
	}
	snapshot, err := h.accounts.Load()
	if err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Business credential vault is unavailable")
		return
	}
	if input.Revision != snapshot.Revision {
		contract.Error(w, 409, "ABORTED", "Account revision changed; refresh before collecting")
		return
	}
	if input.ID != "" && !slices.ContainsFunc(snapshot.Accounts, func(a vault.Account) bool { return a.ID == input.ID }) {
		contract.Error(w, 404, "NOT_FOUND", "Business account does not exist")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	directory, err := filepath.Abs(filepath.Join(filepath.Dir(h.path), "browser"))
	if err != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Business browser directory is unavailable")
		return
	}
	credential, err := h.collector.Collect(ctx, browser.Options{ExecutablePath: input.ExecutablePath, Directory: directory}, h.launcher)
	if err != nil {
		switch {
		case errors.Is(err, browser.ErrBusy):
			contract.Error(w, 409, "ABORTED", "Another Business collection is active")
		case errors.Is(err, browser.ErrCleanup):
			contract.Error(w, 503, "UNAVAILABLE", "Browser cleanup was not confirmed; credentials were not saved")
		case errors.Is(err, browser.ErrVersion):
			contract.Error(w, 412, "FAILED_PRECONDITION", "A tested browser release with the required BiDi capabilities is required")
		case errors.Is(err, context.Canceled):
			contract.Error(w, 499, "CANCELLED", "Business collection cancelled")
		case errors.Is(err, context.DeadlineExceeded):
			contract.Error(w, 504, "DEADLINE_EXCEEDED", "Business collection timed out")
		default:
			contract.Error(w, 503, "UNAVAILABLE", "Business session fields could not be collected")
		}
		return
	}
	defer func() { credential = vault.Credential{} }()
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil || h.closed.Load() {
		contract.Error(w, 499, "CANCELLED", "Business collection cancelled before saving")
		return
	}
	result, err := h.accounts.Update(input.Revision, func(s *vault.Snapshot) error {
		if h.closed.Load() {
			return context.Canceled
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		index := slices.IndexFunc(s.Accounts, func(a vault.Account) bool { return a.ID == input.ID })
		if input.ID != "" && index < 0 {
			return vault.ErrConflict
		}
		if index < 0 {
			s.Accounts = append(s.Accounts, vault.Account{ID: vault.NewID(), Label: strings.TrimSpace(input.Label), Enabled: false, Epoch: 1, Credential: credential})
		} else {
			a := &s.Accounts[index]
			a.Label, a.Credential, a.Enabled = strings.TrimSpace(input.Label), credential, false
			a.Epoch++
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, vault.ErrConflict) {
			contract.Error(w, 409, "ABORTED", "Accounts changed during collection; refresh before retrying")
		} else {
			contract.Error(w, 503, "UNAVAILABLE", "Collected credentials could not be saved")
		}
		return
	}
	h.reconcileAccountsLocked(result)
	writeJSON(w, accountResponse(result))
}
