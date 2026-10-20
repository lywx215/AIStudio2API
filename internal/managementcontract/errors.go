package managementcontract

import "errors"

type ErrorCode string

const (
	CodeAccountBusy             ErrorCode = "ACCOUNT_BUSY"
	CodeAuditPersistenceFailed  ErrorCode = "AUDIT_PERSISTENCE_FAILED"
	CodeCancelled               ErrorCode = "CANCELLED"
	CodeConsolePasswordRequired ErrorCode = "CONSOLE_PASSWORD_REQUIRED"
	CodeDuplicateAccount        ErrorCode = "DUPLICATE_ACCOUNT"
	CodeInvalidRequest          ErrorCode = "INVALID_REQUEST"
	CodeInvalidCredentials      ErrorCode = "INVALID_CREDENTIALS"
	CodeIdempotencyKeyRequired  ErrorCode = "IDEMPOTENCY_KEY_REQUIRED"
	CodePayloadTooLarge         ErrorCode = "PAYLOAD_TOO_LARGE"
	CodeRateLimited             ErrorCode = "RATE_LIMITED"
	CodeUnauthorized            ErrorCode = "UNAUTHORIZED"
	CodeForbidden               ErrorCode = "FORBIDDEN"
	CodeMethodNotAllowed        ErrorCode = "METHOD_NOT_ALLOWED"
	CodeIdempotencyConflict     ErrorCode = "IDEMPOTENCY_CONFLICT"
	CodeInternalError           ErrorCode = "INTERNAL_ERROR"
	CodeInterrupted             ErrorCode = "INTERRUPTED"
	CodeInvalidState            ErrorCode = "INVALID_STATE"
	CodeNotFound                ErrorCode = "NOT_FOUND"
	CodePersistenceError        ErrorCode = "PERSISTENCE_ERROR"
	CodeVersionConflict         ErrorCode = "VERSION_CONFLICT"
	CodeVerificationFailed      ErrorCode = "VERIFICATION_FAILED"
	CodeVerificationTimeout     ErrorCode = "VERIFICATION_TIMEOUT"
)

// ErrorSpec is a closed, case-sensitive wire mapping. Never expose err.Error()
// from another package as a management message.
type ErrorSpec struct {
	Status  int
	Message string
}

var errorCatalog = map[ErrorCode]ErrorSpec{
	CodeAccountBusy:             {409, "Account drain is pending; active requests were not interrupted."},
	CodeAuditPersistenceFailed:  {500, "The operation was committed but its audit record could not be persisted. Read current state before retrying."},
	CodeCancelled:               {409, "Uncommitted work was cancelled."},
	CodeConsolePasswordRequired: {403, "A console-password session is required."},
	CodeDuplicateAccount:        {409, "Account already exists."},
	CodeInvalidRequest:          {400, "Invalid management request."},
	CodeInvalidCredentials:      {400, "Invalid credential storage state."},
	CodeIdempotencyKeyRequired:  {400, "Idempotency-Key is required."},
	CodePayloadTooLarge:         {413, "Management payload exceeds its limit."},
	CodeRateLimited:             {429, "Management task queue is full."},
	CodeUnauthorized:            {401, "A valid management bearer token is required."},
	CodeForbidden:               {403, "Required management permission is missing."},
	CodeMethodNotAllowed:        {405, "Method not allowed."},
	CodeIdempotencyConflict:     {409, "Idempotency key was used with different content."},
	CodeInternalError:           {500, "Management operation failed."},
	CodeInterrupted:             {409, "Work was interrupted and will not be replayed."},
	CodeInvalidState:            {409, "Operation is unavailable in the current state."},
	CodeNotFound:                {404, "Management resource not found."},
	CodePersistenceError:        {500, "Management persistence failed. The operation may already be committed; read current state before retrying."},
	CodeVersionConflict:         {409, "Account changed while this operation was running."},
	CodeVerificationFailed:      {500, "Target account verification failed."},
	CodeVerificationTimeout:     {500, "Target account verification timed out."},
}

func ErrorCatalog() map[ErrorCode]ErrorSpec {
	result := make(map[ErrorCode]ErrorSpec, len(errorCatalog))
	for code, spec := range errorCatalog {
		result[code] = spec
	}
	return result
}

// Error has only the two public fields. Status is derived from its code.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) HTTPStatus() int {
	if spec, ok := errorCatalog[e.Code]; ok {
		return spec.Status
	}
	return 500
}
func NewError(code ErrorCode) *Error {
	spec, ok := errorCatalog[code]
	if !ok {
		code, spec = CodeInternalError, errorCatalog[CodeInternalError]
	}
	return &Error{Code: code, Message: spec.Message}
}

// PublicError discards free-form implementation messages, even on a typed Error.
// Only the frozen catalog is allowed in an HTTP/task/audit error projection.
func PublicError(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return NewError(typed.Code)
	}
	return NewError(CodeInternalError)
}
