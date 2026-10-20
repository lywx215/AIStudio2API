package managementcontract

import (
	"context"
	"slices"
)

type Channel string

const (
	ChannelBuild      Channel = "build"
	ChannelPlayground Channel = "playground"
)

// NormalizeVerificationChannels returns a detached, canonical selection. A nil
// selection is the default; an explicitly empty or duplicate selection is invalid.
func NormalizeVerificationChannels(channels []Channel) ([]Channel, error) {
	if channels == nil {
		return []Channel{ChannelBuild}, nil
	}
	if len(channels) < 1 || len(channels) > 2 {
		return nil, NewError(CodeInvalidRequest)
	}
	seen := map[Channel]bool{}
	for _, channel := range channels {
		if (channel != ChannelBuild && channel != ChannelPlayground) || seen[channel] {
			return nil, NewError(CodeInvalidRequest)
		}
		seen[channel] = true
	}
	out := []Channel{}
	for _, channel := range []Channel{ChannelBuild, ChannelPlayground} {
		if seen[channel] {
			out = append(out, channel)
		}
	}
	return out, nil
}

type VerificationMode string

const (
	VerificationConnection VerificationMode = "connection"
	VerificationModel      VerificationMode = "model"
)

type TestRequest struct {
	Mode     VerificationMode `json:"mode,omitempty"`
	Model    string           `json:"model,omitempty"`
	Channels []Channel        `json:"channels,omitempty"`
}

// VerificationResult extends the original closed Aitoapi schema separately.
// Single-channel transport fields mirror the child; aggregate transport fields
// are absent. Connection evidence can never establish model verification.
// Connection results use connection_verified and are never model evidence.
type VerificationResult struct {
	Success        bool   `json:"success"`
	AuthIndex      int64  `json:"authIndex"`
	Model          string `json:"model"`
	RequestID      string `json:"requestId,omitempty"`
	UpstreamStatus *int   `json:"upstreamStatus"`
	Stage          string `json:"stage"`
	Versions
	ActualChannel  Channel                     `json:"actualChannel,omitempty"`
	Channels       []Channel                   `json:"channels,omitempty"`
	ChannelResults []VerificationChannelResult `json:"channelResults,omitempty"`
}

// VerificationChannelResult is immutable evidence for one explicitly requested
// channel. Empty actualChannel/requestId means that no upstream was observed.
type VerificationChannelResult struct {
	RequestedChannel Channel `json:"requestedChannel"`
	ActualChannel    Channel `json:"actualChannel,omitempty"`
	Success          bool    `json:"success"`
	AuthIndex        int64   `json:"authIndex"`
	Model            string  `json:"model"`
	RequestID        string  `json:"requestId,omitempty"`
	UpstreamStatus   *int    `json:"upstreamStatus"`
	Stage            string  `json:"stage"`
	Versions
	Error *Error `json:"error,omitempty"`
}

// FailFinalization retains observed child evidence when task completion, CAS,
// cancellation or recovery prevents the complete operation from succeeding.
func (v *VerificationResult) FailFinalization(code ErrorCode) {
	if v == nil || len(v.Channels) == 0 {
		return
	}
	v.Success, v.Stage = false, "failed"
	if code == CodeCancelled {
		v.Stage = "cancelled"
	}
	if code == CodeInterrupted {
		v.Stage = "interrupted"
	}
}

// ValidVerificationResult also accepts historical single-channel persisted
// evidence. New aggregate results must have canonical channels and exact identity
// and version attribution for each completed child.
func ValidVerificationResult(v VerificationResult) bool {
	if !ValidModel(v.Model) || v.AuthIndex < 0 || v.AuthIndex > MaxSafeInteger || v.CredentialVersion < 1 || v.StateVersion < 1 || v.CredentialVersion > MaxSafeInteger || v.StateVersion > MaxSafeInteger {
		return false
	}
	if len(v.Channels) == 0 {
		return v.Success && ValidRequestID(v.RequestID) && v.UpstreamStatus != nil && *v.UpstreamStatus >= 200 && *v.UpstreamStatus < 300 && (v.ActualChannel == ChannelBuild || v.ActualChannel == ChannelPlayground) && (v.Stage == "model_verified" || v.Stage == "connection_verified")
	}
	if v.Stage != "verifying" && v.Stage != "failed" && v.Stage != "cancelled" && v.Stage != "interrupted" && v.Stage != "model_verified" && v.Stage != "connection_verified" {
		return false
	}
	if v.Success && v.Stage != "model_verified" && v.Stage != "connection_verified" {
		return false
	}
	channels, err := NormalizeVerificationChannels(v.Channels)
	if err != nil || !slices.Equal(channels, v.Channels) || len(v.ChannelResults) > len(channels) || (v.Success && len(v.ChannelResults) != len(channels)) {
		return false
	}
	all := len(v.ChannelResults) == len(channels)
	for i, r := range v.ChannelResults {
		if r.RequestedChannel != channels[i] || r.AuthIndex != v.AuthIndex || r.Model != v.Model || r.Versions != v.Versions || (r.ActualChannel != "" && r.ActualChannel != r.RequestedChannel) || (r.RequestID != "" && !ValidRequestID(r.RequestID)) || (r.UpstreamStatus != nil && (*r.UpstreamStatus < 100 || *r.UpstreamStatus > 599)) {
			return false
		}
		if r.Success {
			if r.ActualChannel != r.RequestedChannel || !ValidRequestID(r.RequestID) || r.UpstreamStatus == nil || *r.UpstreamStatus < 200 || *r.UpstreamStatus >= 300 || r.Error != nil || (r.Stage != "model_verified" && r.Stage != "connection_verified") {
				return false
			}
		} else {
			all = false
			if r.Error == nil || (r.Stage != "failed" && r.Stage != "cancelled" && r.Stage != "interrupted") {
				return false
			}
		}
	}
	if v.Success && !all {
		return false
	}
	if !v.Success && all && v.Stage != "failed" && v.Stage != "cancelled" && v.Stage != "interrupted" {
		return false
	}
	if len(channels) > 1 {
		return v.ActualChannel == "" && v.RequestID == "" && v.UpstreamStatus == nil
	}
	if len(v.ChannelResults) == 0 {
		return !v.Success && v.ActualChannel == "" && v.RequestID == "" && v.UpstreamStatus == nil
	}
	r := v.ChannelResults[0]
	return v.ActualChannel == r.ActualChannel && v.RequestID == r.RequestID && ((v.UpstreamStatus == nil && r.UpstreamStatus == nil) || (v.UpstreamStatus != nil && r.UpstreamStatus != nil && *v.UpstreamStatus == *r.UpstreamStatus))
}

type VerificationInput struct {
	Account  AccountRecord
	Mode     VerificationMode
	Model    string
	Channels []Channel
	// Checkpoint persists completed child evidence before another channel starts.
	// It is internal and never receives credential material.
	Checkpoint func(VerificationResult) error
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

// VerificationTarget creates an isolated, account- and channel-pinned session.
// Its app adapter must not call the ordinary routing/retry entry point, reuse a
// production worker, choose another account or fall back to another channel.
type VerificationTarget interface {
	Open(context.Context, VerificationInput) (VerificationSession, error)
}
type VerificationProbe struct {
	Mode            VerificationMode
	Model           string
	Prompt          string
	MaxOutputTokens int
	Channel         Channel
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
