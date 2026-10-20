package managementcontract

import "context"

type Channel string

const ChannelPlayground Channel = "playground"

type VerificationMode string

const (
	VerificationConnection VerificationMode = "connection"
	VerificationModel      VerificationMode = "model"
)

type TestRequest struct {
	Mode  VerificationMode `json:"mode,omitempty"`
	Model string           `json:"model,omitempty"`
}

// VerificationResult is the Go Playground extension of Aitoapi's closed schema.
// actualChannel is required here and is NOT added to the original closed schema.
// Connection results use connection_verified and are never model evidence.
type VerificationResult struct {
	Success        bool   `json:"success"`
	AuthIndex      int64  `json:"authIndex"`
	Model          string `json:"model"`
	RequestID      string `json:"requestId"`
	UpstreamStatus *int   `json:"upstreamStatus"`
	Stage          string `json:"stage"`
	Versions
	ActualChannel Channel `json:"actualChannel"`
}
type VerificationInput struct {
	Account AccountRecord
	Mode    VerificationMode
	Model   string
	// nil selects a snapshot of this account; non-nil is an isolated replacement
	// candidate. No candidate is published by the verifier.
	Candidate *CredentialMaterial
}
type VerificationOutput struct {
	Result             VerificationResult
	RefreshedCandidate *CredentialMaterial
}

type Verifier interface {
	Verify(context.Context, VerificationInput, ProgressFunc) (VerificationOutput, error)
	Close(context.Context) error
}

// VerificationTarget creates an isolated, account-pinned Playground session.
// Its app adapter must not call the ordinary routing/retry entry point, reuse a
// production worker, choose another account or fall back to Build.
type VerificationTarget interface {
	Open(context.Context, VerificationInput) (VerificationSession, error)
}
type VerificationProbe struct {
	Mode            VerificationMode
	Model           string
	Prompt          string
	MaxOutputTokens int
}
type VerificationObservation struct {
	AccountID      string
	NativeID       string
	ActualChannel  Channel
	Model          string
	RequestID      string
	UpstreamStatus int
	VisibleText    string
	Finished       bool
	// IdentityConfirmed refers to reliable session identity, not a free-form
	// email regex. Lack of proof cannot be upgraded to proven account ownership.
	IdentityConfirmed bool
}
type VerificationSession interface {
	Run(context.Context, VerificationProbe, ProgressFunc) (VerificationObservation, error)
	Close() error
}
type VerifierOptions struct {
	Target      VerificationTarget
	Clock       Clock
	NewID       IDGenerator
	Concurrency int
}
