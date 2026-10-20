// Package managementcontract defines the frozen management protocol and the
// dependency seams shared by storage, account coordination, HTTP and verification.
// It depends only on the Go standard library and must not import api, app,
// aistudio or any management implementation package.
//
// Implementations are process scoped. In particular a generation restart must
// never recreate keys, task queues, idempotency records, versions or receipts.
// See docs/management/contract.md for commit/recovery and admission requirements.
package managementcontract

import "time"

const (
	ProtocolVersion     = "1.0.0"
	Namespace           = "/api/manage/v1"
	KeyNamespace        = "/api/management-keys"
	DefaultModel        = "gemini-3.8-flash"
	DefaultPageLimit    = 50
	MaxPageLimit        = 200
	MaxBatchItems       = 100
	MaxBodyBytes        = 10 << 20
	MaxCredentialBytes  = 1 << 20
	MaxSafeInteger      = 9007199254740991
	Retention           = 30 * 24 * time.Hour
	DrainTimeout        = 60 * time.Second
	VerificationTimeout = 10 * time.Minute
	MaintenanceInterval = time.Hour
	MaintenanceTimeout  = 30 * time.Second
	PruneBatchRows      = 100
)

type Clock func() time.Time
type IDGenerator func(prefix string) (string, error)
type ProgressFunc func(stage string, progress int)

type Scope string

const (
	ScopeSystemRead      Scope = "system:read"
	ScopeAccountsRead    Scope = "accounts:read"
	ScopeAccountsWrite   Scope = "accounts:write"
	ScopeAccountsTest    Scope = "accounts:test"
	ScopeAccountsExport  Scope = "accounts:export"
	ScopeAccountsArchive Scope = "accounts:archive"
	ScopeSettingsRead    Scope = "settings:read"
	ScopeSettingsWrite   Scope = "settings:write"
	ScopeUsageRead       Scope = "usage:read"
	ScopeAuditRead       Scope = "audit:read"
	ScopeTasksRead       Scope = "tasks:read"
	ScopeTasksWrite      Scope = "tasks:write"
)

// Scopes returns a fresh copy in stable protocol order.
func Scopes() []Scope {
	return []Scope{ScopeSystemRead, ScopeAccountsRead, ScopeAccountsWrite,
		ScopeAccountsTest, ScopeAccountsExport, ScopeAccountsArchive,
		ScopeSettingsRead, ScopeSettingsWrite, ScopeUsageRead, ScopeAuditRead,
		ScopeTasksRead, ScopeTasksWrite}
}

// ScopeTemplate returns a fresh copy. Unknown names have no permissions.
func ScopeTemplate(name string) []Scope {
	var result []Scope
	for _, scope := range Scopes() {
		read := scope == ScopeSystemRead || scope == ScopeAccountsRead ||
			scope == ScopeSettingsRead || scope == ScopeUsageRead || scope == ScopeTasksRead
		operator := read || scope == ScopeAccountsWrite || scope == ScopeAccountsTest || scope == ScopeTasksWrite
		if name == "admin" || name == "readonly" && (read || scope == ScopeAuditRead) || name == "operator" && operator {
			result = append(result, scope)
		}
	}
	return result
}
