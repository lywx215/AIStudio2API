// Package dispatch classifies requests without consuming their bodies or
// consulting an upstream. An unmatched request belongs to the original handler.
package dispatch

import (
	"net/http"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

type Target uint8

const (
	Legacy Target = iota
	Public
	UploadSession
	Admin
	Invalid
)

func ModelID(path string) string {
	if !strings.HasPrefix(path, "/v1beta/models/") {
		return ""
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
	return id
}

// Match gives existing aliases priority, even when the caller supplies the
// provider header. ownsLegacy must inspect an in-memory snapshot only.
func Match(r *http.Request, ownsLegacy func(string) bool) Target {
	path := r.URL.Path
	if path == "/api/addons/business" || strings.HasPrefix(path, "/api/addons/business/") {
		return Admin
	}
	// Never let a routing header capture another management or static endpoint.
	if !strings.HasPrefix(path, "/v1/") && !strings.HasPrefix(path, "/v1beta/") && !strings.HasPrefix(path, "/upload/") {
		return Legacy
	}
	id := ModelID(path)
	selector := r.Header.Get(contract.ProviderHeader)
	businessModel := strings.HasPrefix(id, "business-")
	if id != "" && !businessModel && selector == "" {
		return Legacy
	}
	if id != "" && ownsLegacy != nil && ownsLegacy(id) {
		return Legacy
	}
	businessFile := strings.HasPrefix(path, "/v1beta/files/biz-")
	if path == "/upload/v1beta/files" && r.URL.Query().Has("upload_id") {
		if strings.HasPrefix(r.URL.Query().Get("upload_id"), "biz-") {
			if selector != "" && selector != "business" {
				return Invalid
			}
			return UploadSession
		}
		// An old upload capability keeps its original interpretation.
		return Legacy
	}
	if selector != "" && selector != "business" {
		return Invalid
	}
	if selector == "business" || businessModel || businessFile {
		if id != "" && !businessModel {
			return Invalid
		}
		return Public
	}
	return Legacy
}
