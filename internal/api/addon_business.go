package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/dispatch"
	"github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// BusinessAddon attaches optional handlers to the fully assembled native API.
// Upload must authenticate an issued, expiring upload capability itself: a
// biz- prefix is routing information, never authorization.
type BusinessAddon struct {
	Public     http.Handler
	Admin      http.Handler
	Upload     http.Handler
	OwnsLegacy func(string) bool
	Principal  func(string) (string, error)
}

func attachBusiness(native http.Handler, config Config, consoleAuth *adminAuth) http.Handler {
	addon := config.Business
	if addon == nil {
		return native
	}
	unavailable := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contract.Error(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Business module is unavailable")
	})
	public, admin, upload := addon.Public, addon.Admin, addon.Upload
	if public == nil {
		public = unavailable
	}
	if admin == nil {
		admin = unavailable
	}
	if upload == nil {
		upload = unavailable
	}
	// A process-local secret prevents public key hashes from becoming a key
	// guessing oracle. Persisted principal-owned resources need a durable salt.
	salt := make([]byte, 32)
	_, _ = rand.Read(salt)
	identify := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := "single-tenant"
			modelKey := ""
			for _, key := range strings.Split(config.APIKey, ",") {
				key = strings.TrimSpace(key)
				if key != "" && checkAPIKeyMatches(r, []string{key}) {
					modelKey = key
					mac := hmac.New(sha256.New, salt)
					_, _ = mac.Write([]byte(key))
					principal = hex.EncodeToString(mac.Sum(nil))
					break
				}
			}
			if addon.Principal != nil {
				var err error
				principal, err = addon.Principal(modelKey)
				if err != nil || principal == "" {
					contract.Error(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Business file identity is unavailable")
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(contract.WithPrincipal(r.Context(), principal)))
		})
	}
	protect := func(next http.Handler) http.Handler {
		return corsMiddleware(bodyLimitMiddleware(browserOriginMiddleware(config.APIKey,
			authMiddleware(config.APIKey, identify(next)))))
	}
	public = protect(public)
	admin = consoleAuth.handler(admin)
	upload = corsMiddleware(bodyLimitMiddleware(browserOriginMiddleware(config.APIKey, upload)))
	invalid := protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contract.Error(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Conflicting or unknown Business routing selector")
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if managementcontract.IsManagementPath(r.URL.Path) {
			native.ServeHTTP(w, r)
			return
		}
		switch dispatch.Match(r, addon.OwnsLegacy) {
		case dispatch.Public:
			public.ServeHTTP(w, r)
		case dispatch.Admin:
			admin.ServeHTTP(w, r)
		case dispatch.UploadSession:
			upload.ServeHTTP(w, r)
		case dispatch.Invalid:
			invalid.ServeHTTP(w, r)
		default:
			native.ServeHTTP(w, r)
		}
	})
}
