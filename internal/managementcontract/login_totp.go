package managementcontract

import "context"

const AuditLoginTotpWrite = "login-totp.write"
const AuditLoginTotpClear = "login-totp.clear"

// LoginTotpStatus is the only TOTP projection allowed on public APIs.
type LoginTotpStatus struct {
	Configured      bool   `json:"configured"`
	Version         uint64 `json:"version"`
	LastOperationID string `json:"lastOperationId"`
}

type LoginTotpInput struct {
	Email           string  `json:"email"`
	Secret          *string `json:"twofa_secret,omitempty"`
	ExpectedVersion *uint64 `json:"expectedVersion"`
	OperationID     string  `json:"operationId"`
}

func (LoginTotpInput) String() string   { return "[login TOTP input]" }
func (LoginTotpInput) GoString() string { return "[login TOTP input]" }

// Optional extension keeps existing management adapters compatible.
type LoginTotpCoordinator interface {
	LoginTotp(context.Context, string) (LoginTotpStatus, error)
	WriteLoginTotp(context.Context, string, LoginTotpInput, bool) (LoginTotpStatus, error)
}
