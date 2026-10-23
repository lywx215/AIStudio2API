// Package host owns only Business lifecycle and resources. Construction is
// offline; failures here must not prevent the original listener from starting.
package host

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/browser"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/fileapi"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/files"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/gemini"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/observe"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type Host struct {
	mu                sync.RWMutex
	path              string
	config            config.Config
	problem           string
	closed            atomic.Bool
	ownsLegacy        func(string) bool
	accounts          *vault.Store
	engine            *core.Engine
	transport         http.RoundTripper
	accountProblem    string
	ctx               context.Context
	cancel            context.CancelFunc
	fileStore         *files.Store
	fileAPI           *fileapi.Handler
	budget            *resources.Budget
	collector         browser.Manager
	launcher          browser.Launcher
	operationMu       sync.Mutex
	operations        sync.WaitGroup
	generating        int
	generationContext context.Context
	generationCancel  context.CancelFunc
	remoteMu          sync.Mutex
	remote            *browserTask
	remoteLast        *browserTask
	remoteCleanup     error
	legacyCollect     bool
	observations      *observe.Catalog
	importUndo        *configImportUndo
}

func New(path string, ownsLegacy func(string) bool) *Host {
	c, err := config.Load(path, ownsLegacy)
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{path: path, config: c, ownsLegacy: ownsLegacy, accounts: vault.New(filepath.Join(filepath.Dir(path), "vault")), ctx: ctx, cancel: cancel}
	h.fileStore = files.New(filepath.Join(filepath.Dir(path), "files"))
	h.observations = observe.New(filepath.Join(filepath.Dir(path), "observations.json"))
	h.fileAPI = fileapi.New(h.fileStore)
	if err != nil {
		h.config = config.Default()
		h.problem = err.Error()
	}
	h.budget = resources.New(h.config.MaxInflightBytes)
	if h.config.Enabled && h.problem == "" {
		h.reloadLocked()
	}
	return h
}

func (h *Host) Close(ctx context.Context) error {
	return h.closeOperations(ctx)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

// AdminHandler is protected by the host's existing consoleAuth instance.
func (h *Host) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, done, ok := h.beginOperation(r)
		if !ok {
			// Status remains observable after shutdown without registering work
			// or reopening any runtime/storage resource.
			if r.Method == http.MethodGet && r.URL.Path == "/api/addons/business/status" {
				h.mu.RLock()
				s := h.statusLocked()
				h.mu.RUnlock()
				writeJSON(w, s)
				return
			}
			contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
			return
		}
		defer done()
		r = request
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/addons/business/status":
			h.mu.RLock()
			s := h.statusLocked()
			h.mu.RUnlock()
			writeJSON(w, s)
		case r.Method == "GET" && r.URL.Path == "/api/addons/business/config":
			h.mu.RLock()
			c := h.config.Clone()
			h.mu.RUnlock()
			writeJSON(w, c)
		case r.URL.Path == "/api/addons/business/config/import" || strings.HasPrefix(r.URL.Path, "/api/addons/business/config/import/"):
			h.handleConfigImport(w, r)
		case r.URL.Path == "/api/addons/business/accounts" || r.URL.Path == "/api/addons/business/accounts/delete":
			h.handleAccounts(w, r)
		case r.URL.Path == "/api/addons/business/collect":
			h.handleCollect(w, r)
		case r.URL.Path == "/api/addons/business/browser-info" || r.URL.Path == "/api/addons/business/browser" || strings.HasPrefix(r.URL.Path, "/api/addons/business/browser/"):
			h.handleBrowser(w, r)
		case r.URL.Path == "/api/addons/business/observations" || r.URL.Path == "/api/addons/business/observations/confirm":
			h.handleObservations(w, r)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/addons/business/try/models/"):
			if !strings.HasSuffix(r.URL.Path, ":generateContent") {
				contract.Error(w, 400, "INVALID_ARGUMENT", "Only explicit unary Business trial requests are supported")
				return
			}
			principal, err := h.Principal("\x00console-management")
			if err != nil {
				contract.Error(w, 503, "UNAVAILABLE", "Business file identity is unavailable")
				return
			}
			clone := r.Clone(contract.WithPrincipal(r.Context(), principal))
			url := *r.URL
			url.Path = "/v1beta/models/" + strings.TrimPrefix(r.URL.Path, "/api/addons/business/try/models/")
			clone.URL = &url
			h.PublicHandler().ServeHTTP(w, clone)
		case r.Method == "PUT" && r.URL.Path == "/api/addons/business/config":
			c, err := config.Decode(r.Body)
			if err == nil {
				err = c.Validate(h.ownsLegacy)
			}
			if err != nil {
				contract.Error(w, 400, "INVALID_ARGUMENT", err.Error())
				return
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.closed.Load() {
				contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
				return
			}
			// A damaged existing file is never silently overwritten with defaults.
			if h.problem != "" {
				contract.Error(w, 409, "FAILED_PRECONDITION", "Repair the Business configuration file before saving")
				return
			}
			if c.Revision != h.config.Revision {
				contract.Error(w, 409, "ABORTED", "Configuration changed; refresh before saving")
				return
			}
			c.Revision++
			if err := config.Save(h.path, c); err != nil {
				contract.Error(w, 500, "INTERNAL", "Business configuration could not be saved")
				return
			}
			changes := c.ChangesFrom(h.config)
			h.config = c.Clone()
			if changes.Resources {
				h.budget.SetLimit(c.MaxInflightBytes)
			}
			if changes.Activation {
				h.reloadLocked()
			} else if changes.Scheduling && h.engine != nil {
				h.engine.Configure(c.MaxConcurrent, time.Duration(c.RequestTimeoutSeconds)*time.Second)
			}
			writeJSON(w, c)
		default:
			contract.Error(w, 404, "NOT_FOUND", "Unknown Business management endpoint")
		}
	})
}

// PublicHandler owns explicit Business requests, including disabled or unknown
// models. They must never fall back to an AI Studio account.
func (h *Host) PublicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, done, ok := h.beginOperation(r)
		if !ok {
			contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
			return
		}
		defer done()
		r = request
		h.mu.RLock()
		s := h.statusLocked()
		c, problem, engine, generationContext := h.config.Clone(), h.problem, h.engine, h.generationContext
		h.mu.RUnlock()
		if s.State == "disabled" {
			contract.Error(w, 503, "UNAVAILABLE", "Business is disabled")
			return
		}
		if problem != "" || h.closed.Load() {
			contract.Error(w, 503, "UNAVAILABLE", "Business is unavailable")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1beta/files") || r.URL.Path == "/upload/v1beta/files" {
			ctx, lease := h.budget.Begin(r.Context())
			defer lease.Close()
			h.fileAPI.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, ":generateContent") || strings.HasSuffix(r.URL.Path, ":streamGenerateContent")) {
			if r.ContentLength > gemini.MaxRequestBytes {
				contract.Error(w, 413, "RESOURCE_EXHAUSTED", "Business request body is too large")
				return
			}
			if engine == nil || generationContext == nil {
				contract.Error(w, 503, "UNAVAILABLE", "No eligible Business account is available")
				return
			}
			ctx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(generationContext, cancel)
			defer cancel()
			defer stop()
			if generationContext.Err() != nil {
				cancel()
			}
			r = r.WithContext(ctx)
			// Bound JSON/base64 parsing and immutable file snapshots as well as
			// the later upstream lease. Reject excess work before reading a body.
			h.mu.Lock()
			if h.generating >= h.config.MaxConcurrent {
				h.mu.Unlock()
				contract.Error(w, 429, "RESOURCE_EXHAUSTED", "Business request concurrency limit reached")
				return
			}
			h.generating++
			h.mu.Unlock()
			defer func() { h.mu.Lock(); h.generating--; h.mu.Unlock() }()
			budgetContext, lease := h.budget.Begin(r.Context())
			defer lease.Close()
			if _, err := lease.Reserve(resources.GenerationReserveBytes); err != nil {
				gemini.Error(w, err)
				return
			}
			r = r.WithContext(budgetContext)
		}
		bindings := make([]config.Binding, 0, len(c.Bindings))
		for _, binding := range c.Bindings {
			if h.ownsLegacy == nil || !h.ownsLegacy(binding.PublicID) {
				bindings = append(bindings, binding)
			}
		}
		var generator contract.Generator
		if engine != nil {
			generator = engine
		}
		gemini.HandlerWithFiles(bindings, generator, h.fileAPI.Resolver(r)).ServeHTTP(w, r)
	})
}

func (h *Host) UploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, done, ok := h.beginOperation(r)
		if !ok {
			contract.Error(w, 503, "UNAVAILABLE", "Business is closed")
			return
		}
		defer done()
		r = request
		h.mu.RLock()
		enabled, problem := h.config.Enabled, h.problem
		h.mu.RUnlock()
		if !enabled || problem != "" || h.closed.Load() {
			contract.Error(w, 503, "UNAVAILABLE", "Business is disabled or unavailable")
			return
		}
		ctx, lease := h.budget.Begin(r.Context())
		defer lease.Close()
		h.fileAPI.UploadHandler().ServeHTTP(w, r.WithContext(ctx))
	})
}
