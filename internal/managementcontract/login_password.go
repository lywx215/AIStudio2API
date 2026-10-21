package managementcontract

import "context"

const AuditLoginPasswordWrite = "login-password.write"
const AuditLoginPasswordClear = "login-password.clear"

// LoginPasswordStatus is the only password projection allowed on public APIs.
type LoginPasswordStatus struct {
	Configured      bool   `json:"configured"`
	Version         uint64 `json:"version"`
	LastOperationID string `json:"lastOperationId"`
}

type LoginPasswordInput struct {
	Email           string  `json:"email"`
	Password        *string `json:"password,omitempty"`
	ExpectedVersion *uint64 `json:"expectedVersion"`
	OperationID     string  `json:"operationId"`
}

func (LoginPasswordInput) String() string   { return "[login password input]" }
func (LoginPasswordInput) GoString() string { return "[login password input]" }

// Optional extension keeps existing management adapters compatible.
type LoginPasswordCoordinator interface {
	LoginPassword(context.Context, string) (LoginPasswordStatus, error)
	WriteLoginPassword(context.Context, string, LoginPasswordInput, bool) (LoginPasswordStatus, error)
}
