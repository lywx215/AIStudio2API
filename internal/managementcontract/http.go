package managementcontract

import (
	"context"
	"net/http"
	"strings"
)

// ConsoleAuthorizer is supplied by api's existing console session owner. A
// disabled-login loopback request, bearer/model key, pairing token or old session
// is never sufficient. HTTP owns bounded parsing, same-origin/CSRF checks and
// the management envelope for authorization failures.
type ConsoleAuthorizer interface{ AuthorizeConsole(*http.Request) error }
type ConsoleAuthorizeFunc func(*http.Request) error

func (f ConsoleAuthorizeFunc) AuthorizeConsole(r *http.Request) error { return f(r) }

// HTTPRegistrar owns full paths (no StripPrefix). Both exact namespace roots and
// subtrees must be mounted before native console authentication. Each handler
// terminates unknown routes/methods with management JSON; no redirects or HTML.
type HTTPRegistrar interface {
	Handler() http.Handler
	KeyHandler(ConsoleAuthorizer) http.Handler
}

// IsManagementPath includes exact roots to avoid ServeMux slash/clean redirects
// before the management handler can return its terminal JSON error.
func IsManagementPath(path string) bool {
	return path == Namespace || strings.HasPrefix(path, Namespace+"/") ||
		path == KeyNamespace || strings.HasPrefix(path, KeyNamespace+"/")
}

type HTTPOptions struct {
	Store      Store
	Tasks      TaskService
	Accounts   AccountCoordinator
	Projection Projection
	Clock      Clock
	NewID      IDGenerator
}

// ProcessServices is owned by runtimeManager, not runtimeGeneration. It is a
// dependency bundle, not a second store or a scheduler implementation. G0 fills
// it once during final composition, recovers before publishing accounts, starts
// tasks once and closes tasks/verifier/store only on process shutdown.
type ProcessServices struct {
	Store    Store
	Accounts AccountCoordinator
	Tasks    TaskService
	Verifier Verifier
	HTTP     HTTPRegistrar
}

type Check struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}
type Readiness struct {
	Ready  bool    `json:"ready"`
	Checks []Check `json:"checks"`
}
type Capabilities struct {
	Backend                     string    `json:"backend"`
	ProtocolVersion             string    `json:"protocolVersion"`
	StableAccountIDs            bool      `json:"stableAccountIds"`
	DualCAS                     bool      `json:"dualCAS"`
	UploadReceipts              bool      `json:"uploadReceipts"`
	TargetedVerification        bool      `json:"targetedVerification"`
	VerificationChannels        []Channel `json:"verificationChannels"`
	DefaultVerificationChannels []Channel `json:"defaultVerificationChannels"`
	MultiChannelVerification    bool      `json:"multiChannelVerification"`
	SettingsWrite               bool      `json:"settingsWrite"`
}
type SystemStatus struct {
	Ready               bool   `json:"ready"`
	BrowserConnected    bool   `json:"browserConnected"`
	IsSystemBusy        bool   `json:"isSystemBusy"`
	AccountCount        int    `json:"accountCount"`
	EnabledAccountCount int    `json:"enabledAccountCount"`
	ActiveContextsCount int    `json:"activeContextsCount"`
	ServiceState        string `json:"serviceState"`
	WAABackend          string `json:"waaBackend"`
}
type SettingsValues struct {
	WAABackend             string   `json:"waaBackend"`
	WarmWorkerLimit        int      `json:"warmWorkerLimit"`
	MaxActiveWorkers       int      `json:"maxActiveWorkers"`
	WarmStartupConcurrency int      `json:"warmStartupConcurrency"`
	PerAccountConcurrency  int      `json:"perAccountConcurrency"`
	RoutingStrategy        string   `json:"routingStrategy"`
	UpstreamChannels       []string `json:"upstreamChannels"`
}
type SettingsSnapshot struct {
	Values         SettingsValues `json:"values"`
	PersistentKeys []string       `json:"persistentKeys"`
}
type SettingsPatch struct {
	WarmWorkerLimit        *int    `json:"warmWorkerLimit,omitempty"`
	MaxActiveWorkers       *int    `json:"maxActiveWorkers,omitempty"`
	WarmStartupConcurrency *int    `json:"warmStartupConcurrency,omitempty"`
	PerAccountConcurrency  *int    `json:"perAccountConcurrency,omitempty"`
	RoutingStrategy        *string `json:"routingStrategy,omitempty"`
}
type SettingsUpdated struct {
	Values    SettingsValues `json:"values"`
	Persisted bool           `json:"persisted"`
	Applied   bool           `json:"applied"`
}
type AccountRuntimeState struct {
	AccountID      string     `json:"accountId"`
	State          string     `json:"state"`
	CooldownUntil  *Timestamp `json:"cooldownUntil"`
	ActiveRequests int        `json:"activeRequests"`
}
type Diagnostics struct {
	ServiceState string                    `json:"serviceState"`
	WAABackend   string                    `json:"waaBackend"`
	Accounts     Page[AccountRuntimeState] `json:"accounts"`
}
type UsageRecord struct {
	RequestID  string    `json:"requestId"`
	AccountID  *string   `json:"accountId"`
	Index      *int64    `json:"index"`
	Model      *string   `json:"model"`
	Outcome    string    `json:"outcome"`
	StatusCode *int      `json:"statusCode"`
	DurationMS int64     `json:"durationMs"`
	StartedAt  Timestamp `json:"startedAt"`
	FinishedAt Timestamp `json:"finishedAt"`
}

// Projection is implemented by app using explicit allowlists. No native config,
// raw logs, request/response body, URL with userinfo, key/hash, cookie or proxy
// password can be returned through these DTOs. Unsupported settings mutations
// return INVALID_STATE and advertise settingsWrite:false.
type Projection interface {
	Status(context.Context) (SystemStatus, error)
	Readiness(context.Context) (Readiness, error)
	Capabilities(context.Context) (Capabilities, error)
	Settings(context.Context) (SettingsSnapshot, error)
	PatchSettings(context.Context, SettingsPatch) (SettingsUpdated, error)
	Diagnostics(context.Context, PageQuery) (Diagnostics, error)
	Usage(context.Context, PageQuery) (Page[UsageRecord], error)
}
