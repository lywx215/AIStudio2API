package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	p "github.com/Mag1cFall/AIStudio2API/internal/proxymanager"
)

func proxyJSON(r *http.Request, value any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return &p.Error{Code: "INVALID_REQUEST"}
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return &p.Error{Code: "INVALID_REQUEST"}
	}
	return nil
}
func proxyAPIError(w http.ResponseWriter, err error) {
	code := p.Code(err)
	status := http.StatusBadRequest
	switch {
	case strings.Contains(code, "CONFLICT"), strings.Contains(code, "CHANGED"), code == "TEST_JOB_ALREADY_RUNNING":
		status = http.StatusConflict
	case strings.Contains(code, "NOT_FOUND"):
		status = http.StatusNotFound
	case strings.Contains(code, "STORAGE"), strings.Contains(code, "UNAVAILABLE"):
		status = http.StatusServiceUnavailable
	}
	writeAdminError(w, status, code, code)
}
func proxyQuery(r *http.Request) (p.Query, error) {
	v := r.URL.Query()
	q := p.Query{Search: v.Get("search"), SourceID: v.Get("source_id"), Protocol: v.Get("protocol"), State: v.Get("state"), Eligible: v.Get("eligible") == "true", Limit: 50}
	var err error
	if v.Get("limit") != "" {
		q.Limit, err = strconv.Atoi(v.Get("limit"))
		if err != nil {
			return q, &p.Error{Code: "INVALID_QUERY"}
		}
	}
	if v.Get("offset") != "" {
		q.Offset, err = strconv.Atoi(v.Get("offset"))
	}
	if err != nil {
		return q, &p.Error{Code: "INVALID_QUERY"}
	}
	return q, nil
}
func (s *server) registerProxyAdmin(mux *http.ServeMux, auth *adminAuth) {
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.config.AdminAuthEnabled || auth.session(r) == nil {
				writeAdminError(w, http.StatusForbidden, "CONSOLE_PASSWORD_REQUIRED", "Console password session required")
				return
			}
			if s.config.ProxyAdmin == nil {
				writeAdminError(w, http.StatusServiceUnavailable, "PROXY_STORAGE_UNAVAILABLE", "Proxy catalog unavailable")
				return
			}
			handler(w, r)
		})
	}
	register("GET /api/proxies/sources", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.config.ProxyAdmin.Sources(r.Context())
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var in p.SourceInput
		if err := proxyJSON(r, &in); err != nil {
			proxyAPIError(w, err)
			return
		}
		item, err := s.config.ProxyAdmin.SaveSource(r.Context(), r.PathValue("id"), in)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, item)
	}
	register("POST /api/proxies/sources", save)
	register("PUT /api/proxies/sources/{id}", save)
	register("DELETE /api/proxies/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if err := proxyJSON(r, &in); err != nil {
			proxyAPIError(w, err)
			return
		}
		if err := s.config.ProxyAdmin.DeleteSource(r.Context(), r.PathValue("id"), in.Revision); err != nil {
			proxyAPIError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	register("POST /api/proxies/sources/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		count, invalid, err := s.config.ProxyAdmin.Refresh(r.Context(), r.PathValue("id"))
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"count": count, "invalid": invalid})
	})
	register("GET /api/proxies", func(w http.ResponseWriter, r *http.Request) {
		q, err := proxyQuery(r)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		page, err := s.config.ProxyAdmin.List(r.Context(), q)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, page)
	})
	register("GET /api/proxies/stats", func(w http.ResponseWriter, r *http.Request) {
		stats, err := s.config.ProxyAdmin.Stats(r.Context())
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, stats)
	})
	register("PATCH /api/proxies/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled  bool  `json:"enabled"`
			Revision int64 `json:"revision"`
		}
		if err := proxyJSON(r, &in); err != nil {
			proxyAPIError(w, err)
			return
		}
		if err := s.config.ProxyAdmin.SetEnabled(r.Context(), r.PathValue("id"), in.Enabled, in.Revision); err != nil {
			proxyAPIError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	register("POST /api/proxies/account-contexts", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		if err := proxyJSON(r, &in); err != nil {
			proxyAPIError(w, err)
			return
		}
		items, err := s.config.ProxyAdmin.AccountContexts(r.Context(), in.IDs)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	register("POST /api/proxies/jobs", func(w http.ResponseWriter, r *http.Request) {
		var in p.JobInput
		if err := proxyJSON(r, &in); err != nil {
			proxyAPIError(w, err)
			return
		}
		job, err := s.config.ProxyAdmin.Submit(r.Context(), r.Header.Get("Idempotency-Key"), in)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})
	register("GET /api/proxies/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := s.config.ProxyAdmin.Jobs(r.Context())
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": jobs})
	})
	register("GET /api/proxies/jobs/by-key/{key}", func(w http.ResponseWriter, r *http.Request) {
		job, err := s.config.ProxyAdmin.FindJob(r.Context(), r.PathValue("key"))
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, job)
	})
	register("GET /api/proxies/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		q, err := proxyQuery(r)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		job, err := s.config.ProxyAdmin.Job(r.Context(), r.PathValue("id"), q.Offset, q.Limit)
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, job)
	})
	register("POST /api/proxies/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := s.config.ProxyAdmin.Cancel(r.Context(), r.PathValue("id")); err != nil {
			proxyAPIError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	register("POST /api/proxies/jobs/{id}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		job, err := s.config.ProxyAdmin.Reconcile(r.Context(), r.PathValue("id"))
		if err != nil {
			proxyAPIError(w, err)
			return
		}
		writeJSON(w, 200, job)
	})
}
