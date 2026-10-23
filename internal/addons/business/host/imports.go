package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

type configImportUndo struct {
	ID        string
	Revision  uint64
	Previous  config.Config
	ConsoleID string
	Lifetime  context.Context
}

func (h *Host) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	console, ok := contract.ConsoleFrom(r.Context())
	if !ok {
		contract.Error(w, 401, "UNAUTHENTICATED", "A console session is required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		contract.Error(w, 405, "INVALID_ARGUMENT", "Method not allowed")
		return
	}
	const prefix = "/api/addons/business/config/import"
	previewOnly, rollback := r.URL.Path == prefix+"/preview", r.URL.Path == prefix+"/rollback"
	if !previewOnly && !rollback && r.URL.Path != prefix {
		contract.Error(w, 404, "NOT_FOUND", "Unknown Business import endpoint")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, config.MaxBytes+2049))
	if err != nil || len(data) > config.MaxBytes+2048 || config.ValidateImportJSON(data) != nil {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business import request")
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	allowed := map[string]bool{"revision": true}
	if rollback {
		allowed["rollback_id"] = true
	} else {
		allowed["source"] = true
		if !previewOnly {
			allowed["source_hash"] = true
		}
	}
	for field := range fields {
		if !allowed[field] {
			contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business import request")
			return
		}
	}
	var request struct {
		Revision   *uint64         `json:"revision"`
		Source     json.RawMessage `json:"source"`
		SourceHash string          `json:"source_hash"`
		RollbackID string          `json:"rollback_id"`
	}
	if json.Unmarshal(data, &request) != nil || request.Revision == nil {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business import request")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed.Load() || r.Context().Err() != nil || console.Lifetime.Err() != nil {
		contract.Error(w, 503, "UNAVAILABLE", "Business is closed or request was cancelled")
		return
	}
	if h.problem != "" {
		contract.Error(w, 409, "FAILED_PRECONDITION", "Repair the Business configuration file before importing")
		return
	}
	if *request.Revision != h.config.Revision || h.config.Revision == ^uint64(0) {
		contract.Error(w, 409, "ABORTED", "Configuration changed; refresh before importing")
		return
	}
	if rollback {
		undo := h.importUndo
		if undo == nil || request.RollbackID == "" || request.RollbackID != undo.ID || undo.Revision != h.config.Revision || undo.ConsoleID != console.ID || undo.Lifetime.Err() != nil {
			contract.Error(w, 409, "ABORTED", "Import rollback is unavailable or configuration changed")
			return
		}
		previous := undo.Previous.Clone()
		previous.Revision = h.config.Revision + 1
		if !config.ImportFitsStorage(previous) {
			contract.Error(w, 409, "FAILED_PRECONDITION", "Import rollback exceeds configuration size limit")
			return
		}
		if err := config.Save(h.path, previous); err != nil {
			contract.Error(w, 500, "INTERNAL", "Business configuration could not be saved")
			return
		}
		h.config, h.importUndo = previous, nil
		writeJSON(w, previous)
		return
	}
	preview, err := config.PreviewImport(h.config, request.Source)
	if err != nil {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business model import source")
		return
	}
	if previewOnly {
		writeJSON(w, preview)
		return
	}
	if request.SourceHash != preview.SourceHash {
		contract.Error(w, 409, "ABORTED", "Import source changed; preview again before applying")
		return
	}
	if len(preview.Added) == 0 {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Import contains no new model bindings")
		return
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		contract.Error(w, 500, "INTERNAL", "Business import could not be prepared")
		return
	}
	previous := h.config.Clone()
	candidate := previous.Clone()
	candidate.Revision++
	candidate.Bindings = append(candidate.Bindings, preview.Added...)
	if candidate.Validate(h.ownsLegacy) != nil || !config.ImportFitsStorage(candidate) {
		contract.Error(w, 400, "INVALID_ARGUMENT", "Invalid Business model import source")
		return
	}
	if err := config.Save(h.path, candidate); err != nil {
		contract.Error(w, 500, "INTERNAL", "Business configuration could not be saved")
		return
	}
	// All additions are disabled. Activation, resources, account leases and
	// existing bindings remain unchanged, so no engine reload is required.
	h.config = candidate.Clone()
	h.importUndo = &configImportUndo{ID: hex.EncodeToString(token[:]), Revision: candidate.Revision, Previous: previous, ConsoleID: console.ID, Lifetime: console.Lifetime}
	writeJSON(w, struct {
		Config     config.Config `json:"config"`
		RollbackID string        `json:"rollback_id"`
	}{candidate, h.importUndo.ID})
}
