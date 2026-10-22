package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

// reloadLocked reconciles account versions without rebuilding the scheduler.
// Even an off/on transition retains draining leases until they release.
func (h *Host) reloadLocked() {
	h.accountProblem = ""
	if !h.config.Enabled || h.closed.Load() {
		if h.generationCancel != nil {
			h.generationCancel()
		}
		if h.engine != nil {
			h.engine.Configure(h.config.MaxConcurrent, time.Duration(h.config.RequestTimeoutSeconds)*time.Second)
			h.engine.Suspend()
		}
		h.collector.Cancel()
		return
	}
	if h.generationContext == nil || h.generationContext.Err() != nil {
		h.generationContext, h.generationCancel = context.WithCancel(h.ctx)
	}
	snapshot, err := h.accounts.Load()
	if err != nil {
		h.accountProblem = "BUSINESS_VAULT_UNAVAILABLE"
		if h.engine != nil {
			h.engine.Suspend()
		}
		return
	}
	h.reconcileAccountsLocked(snapshot)
}

func (h *Host) reconcileAccountsLocked(snapshot vault.Snapshot) {
	if h.closed.Load() {
		return
	}
	h.accountProblem = ""
	timeout := time.Duration(h.config.RequestTimeoutSeconds) * time.Second
	if !h.config.Enabled {
		// Edits while paused must invalidate evidence for replaced credentials
		// immediately, without constructing or enabling an inactive runtime.
		if h.engine != nil {
			h.engine.Reconcile(snapshot.Accounts, h.config.MaxConcurrent, timeout)
			h.engine.Suspend()
		}
		return
	}
	if h.engine == nil {
		if len(snapshot.Accounts) > 0 {
			h.engine = core.New(h.ctx, snapshot.Accounts, h.config.MaxConcurrent, timeout, h.transport)
		}
	} else {
		h.engine.Reconcile(snapshot.Accounts, h.config.MaxConcurrent, timeout)
	}
}

type accountView struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Enabled       bool   `json:"enabled"`
	Epoch         uint64 `json:"epoch"`
	CredentialSet bool   `json:"credential_set"`
}

func accountResponse(snapshot vault.Snapshot) any {
	items := []accountView{}
	for _, a := range snapshot.Accounts {
		items = append(items, accountView{a.ID, a.Label, a.Enabled, a.Epoch, true})
	}
	return map[string]any{"revision": snapshot.Revision, "accounts": items}
}
func (h *Host) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/api/addons/business/accounts" {
		s, err := h.accounts.Load()
		if err != nil {
			contract.Error(w, 503, "UNAVAILABLE", "Business credential vault is unavailable")
			return
		}
		writeJSON(w, accountResponse(s))
		return
	}
	if r.Method != "POST" {
		contract.Error(w, 405, "INVALID_ARGUMENT", "Method not allowed")
		return
	}
	var input struct {
		Revision   uint64            `json:"revision"`
		ID         string            `json:"id"`
		Label      string            `json:"label"`
		Enabled    bool              `json:"enabled"`
		Credential *vault.Credential `json:"credential"`
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business account document")
		return
	}
	if r.Context().Err() != nil {
		contract.Error(w, 499, "CANCELLED", "Business account operation cancelled")
		return
	}
	remove := r.URL.Path == "/api/addons/business/accounts/delete"
	if !remove && (strings.TrimSpace(input.Label) == "" || len(input.Label) > 160) {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Account label is required and limited to 160 bytes")
		return
	}
	if input.Credential != nil && input.Credential.Validate() != nil {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Business credential fields are invalid or missing")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Load() {
		contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
		return
	}
	if r.Context().Err() != nil {
		contract.Error(w, 499, "CANCELLED", "Business account operation cancelled")
		return
	}
	result, err := h.accounts.Update(input.Revision, func(s *vault.Snapshot) error {
		index := slices.IndexFunc(s.Accounts, func(a vault.Account) bool { return a.ID == input.ID })
		if remove {
			if index < 0 {
				return errors.New("account not found")
			}
			s.Accounts = slices.Delete(s.Accounts, index, index+1)
			return nil
		}
		if input.ID != "" && index < 0 {
			return errors.New("account not found")
		}
		if index < 0 {
			if input.Credential == nil {
				return errors.New("credentials required for new account")
			}
			s.Accounts = append(s.Accounts, vault.Account{ID: vault.NewID(), Label: strings.TrimSpace(input.Label), Enabled: input.Enabled, Epoch: 1, Credential: *input.Credential})
			return nil
		}
		a := &s.Accounts[index]
		a.Label = strings.TrimSpace(input.Label)
		a.Enabled = input.Enabled
		if input.Credential != nil {
			// Explicit replacement starts a new epoch, even for equal values.
			// Labels and enabled changes use Snapshot.Revision instead.
			a.Epoch++
			a.Credential = *input.Credential
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, vault.ErrConflict) {
			contract.Error(w, 409, "ABORTED", "Account revision changed; refresh before saving")
		} else if errors.Is(err, vault.ErrUnavailable) {
			contract.Error(w, 503, "UNAVAILABLE", "Business credential vault is unavailable")
		} else {
			contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid account operation")
		}
		return
	}
	h.reconcileAccountsLocked(result)
	writeJSON(w, accountResponse(result))
}
