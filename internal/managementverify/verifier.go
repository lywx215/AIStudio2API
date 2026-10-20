// Package managementverify validates evidence from an isolated, pinned target.
// It never publishes credentials or changes account state.
package managementverify

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

const ProbePrompt = "Reply with OK."
const ProbeMaxOutputTokens = 256

// CandidateSource is an optional private session capability. Material remains
// private until the caller submits it through the coordinator's dual CAS.
// This structural extension does not change the frozen public target interface.
type CandidateSource interface {
	RefreshedCandidate(context.Context) (*c.CredentialMaterial, error)
}

type verifier struct {
	target c.VerificationTarget
	slots  chan struct{}
	mu     sync.Mutex
	closed bool
	active map[*operation]struct{}
	done   chan struct{}
}

type operation struct{ cancel context.CancelFunc }

var _ c.Verifier = (*verifier)(nil)

func New(options c.VerifierOptions) (c.Verifier, error) {
	if options.Target == nil || options.Concurrency < 0 {
		return nil, fmt.Errorf("management verifier requires a target and nonnegative concurrency")
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	return &verifier{target: options.Target, slots: make(chan struct{}, concurrency), active: make(map[*operation]struct{}), done: make(chan struct{})}, nil
}

func (v *verifier) Verify(ctx context.Context, input c.VerificationInput, progress c.ProgressFunc) (out c.VerificationOutput, err error) {
	if input.Mode == "" {
		input.Mode = c.VerificationModel
	}
	var channels []c.Channel
	channels, err = c.NormalizeVerificationChannels(input.Channels)
	if err != nil {
		return out, err
	}
	input.Channels = channels
	if err = validateInput(input); err != nil {
		return out, err
	}
	// Snapshot caller-owned memory before handing it to a potentially long run.
	if input.Candidate != nil {
		material := *input.Candidate
		material.StorageState = append([]byte(nil), material.StorageState...)
		input.Candidate = &material
	}
	ctx, cancel := context.WithTimeout(ctx, c.VerificationTimeout)
	op := &operation{cancel: cancel}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		cancel()
		return out, c.NewError(c.CodeInterrupted)
	}
	v.active[op] = struct{}{}
	v.mu.Unlock()
	defer func() {
		cancel()
		v.mu.Lock()
		delete(v.active, op)
		if v.closed && len(v.active) == 0 {
			select {
			case <-v.done:
			default:
				close(v.done)
			}
		}
		v.mu.Unlock()
	}()
	select {
	case v.slots <- struct{}{}:
		defer func() { <-v.slots }()
	case <-ctx.Done():
		return out, contextError(ctx.Err())
	}
	if err = ctx.Err(); err != nil {
		return out, contextError(err)
	}
	if progress != nil {
		progress("verifying", 10)
	}
	out.Result = c.VerificationResult{AuthIndex: input.Account.Index, Model: input.Model, Versions: input.Account.Versions, Channels: channels, Stage: "verifying"}
	var firstErr error
	for _, channel := range channels {
		single := input
		single.Channels = []c.Channel{channel}
		single.Checkpoint = nil
		child, candidate, childErr := v.verifyChannel(ctx, single, channel, progress)
		out.Result.ChannelResults = append(out.Result.ChannelResults, child)
		if childErr != nil && firstErr == nil {
			firstErr = childErr
		}
		out.Result.Success = firstErr == nil && len(out.Result.ChannelResults) == len(channels)
		if out.Result.Success {
			out.Result.Stage = child.Stage
		} else if firstErr != nil {
			out.Result.Stage = "failed"
		}
		if len(channels) == 1 {
			out.Result.RequestID, out.Result.UpstreamStatus, out.Result.ActualChannel = child.RequestID, child.UpstreamStatus, child.ActualChannel
		}
		if candidate != nil && childErr == nil {
			input.Candidate = candidate
			out.RefreshedCandidate = candidate
		}
		if input.Checkpoint != nil {
			snapshot := out.Result
			snapshot.Channels = slices.Clone(snapshot.Channels)
			snapshot.ChannelResults = slices.Clone(snapshot.ChannelResults)
			if checkpointErr := input.Checkpoint(snapshot); checkpointErr != nil {
				out.Result.Success = false
				out.Result.Stage = "failed"
				return out, c.PublicError(checkpointErr)
			}
		}
		if childErr != nil {
			code := c.PublicError(childErr).Code
			if code == c.CodeCancelled || code == c.CodeVerificationTimeout || code == c.CodeInterrupted || code == c.CodeVersionConflict {
				if code == c.CodeCancelled {
					out.Result.Stage = "cancelled"
				}
				if code == c.CodeInterrupted {
					out.Result.Stage = "interrupted"
				}
				break
			}
		}
	}
	if firstErr != nil {
		out.RefreshedCandidate = nil
		return out, firstErr
	}
	if progress != nil {
		progress(out.Result.Stage, 100)
	}
	return out, nil
}

func (v *verifier) verifyChannel(ctx context.Context, input c.VerificationInput, channel c.Channel, progress c.ProgressFunc) (child c.VerificationChannelResult, candidate *c.CredentialMaterial, err error) {
	child = c.VerificationChannelResult{RequestedChannel: channel, AuthIndex: input.Account.Index, Model: input.Model, Versions: input.Account.Versions, Stage: "failed"}
	defer func() {
		if err != nil {
			child.Success = false
			child.Error = c.PublicError(err)
			child.Stage = "failed"
			if child.Error.Code == c.CodeCancelled {
				child.Stage = "cancelled"
			}
			if child.Error.Code == c.CodeInterrupted {
				child.Stage = "interrupted"
			}
		}
	}()
	if ctx.Err() != nil {
		return child, nil, contextError(ctx.Err())
	}
	session, openErr := v.target.Open(ctx, input)
	if openErr != nil {
		if session != nil {
			_ = session.Close()
		}
		return child, nil, verificationError(ctx, openErr)
	}
	if session == nil {
		return child, nil, c.NewError(c.CodeVerificationFailed)
	}
	var closeOnce sync.Once
	var closeErr error
	closed := make(chan struct{})
	closeSession := func() { closeOnce.Do(func() { closeErr = session.Close(); close(closed) }) }
	stop := context.AfterFunc(ctx, closeSession)
	defer func() {
		stop()
		closeSession()
		<-closed
		if err == nil && closeErr != nil {
			err = c.NewError(c.CodeVerificationFailed)
		}
		if ctx.Err() != nil {
			err = contextError(ctx.Err())
		}
	}()
	obs, runErr := session.Run(ctx, c.VerificationProbe{Mode: input.Mode, Model: input.Model, Channel: channel, Prompt: ProbePrompt, MaxOutputTokens: ProbeMaxOutputTokens}, progress)
	// Only attributable observations become public evidence, even on failure.
	if obs.AccountID == input.Account.AccountID && obs.NativeID == input.Account.NativeID && obs.Model == input.Model && obs.IdentityConfirmed && obs.ActualChannel == channel {
		child.ActualChannel = obs.ActualChannel
		if c.ValidRequestID(obs.RequestID) {
			child.RequestID = obs.RequestID
		}
		if obs.UpstreamStatus >= 100 && obs.UpstreamStatus <= 599 {
			status := obs.UpstreamStatus
			child.UpstreamStatus = &status
		}
	}
	if runErr != nil {
		return child, nil, verificationError(ctx, runErr)
	}
	if ctx.Err() != nil {
		return child, nil, contextError(ctx.Err())
	}
	if !validObservation(input, obs) {
		return child, nil, c.NewError(c.CodeVerificationFailed)
	}
	child.Success, child.Stage = true, "model_verified"
	if input.Mode == c.VerificationConnection {
		child.Stage = "connection_verified"
	}
	if input.Candidate != nil {
		if source, ok := session.(CandidateSource); ok {
			refreshed, candidateErr := source.RefreshedCandidate(ctx)
			if candidateErr != nil {
				return child, nil, verificationError(ctx, candidateErr)
			}
			if refreshed != nil {
				if refreshed.NativeID != input.Account.NativeID || len(refreshed.StorageState) == 0 || len(refreshed.StorageState) > c.MaxCredentialBytes {
					return child, nil, c.NewError(c.CodeVerificationFailed)
				}
				material := *refreshed
				material.StorageState = slices.Clone(refreshed.StorageState)
				candidate = &material
			}
		}
	}
	return child, candidate, nil
}

func validateInput(input c.VerificationInput) error {
	a := input.Account
	if !c.ValidAccountID(a.AccountID) || a.Index < 0 || a.Index > c.MaxSafeInteger || a.NativeID == "" || strings.TrimSpace(a.NativeID) != a.NativeID ||
		a.CredentialVersion < 1 || a.StateVersion < 1 || a.CredentialVersion > c.MaxSafeInteger || a.StateVersion > c.MaxSafeInteger ||
		!c.ValidModel(input.Model) || input.Mode != c.VerificationModel && input.Mode != c.VerificationConnection {
		return c.NewError(c.CodeInvalidRequest)
	}
	if a.Archived || a.Deleted {
		return c.NewError(c.CodeInvalidState)
	}
	if input.Candidate != nil && (input.Candidate.NativeID != a.NativeID || len(input.Candidate.StorageState) == 0 || len(input.Candidate.StorageState) > c.MaxCredentialBytes) {
		return c.NewError(c.CodeInvalidCredentials)
	}
	return nil
}

func validObservation(input c.VerificationInput, obs c.VerificationObservation) bool {
	if obs.AccountID != input.Account.AccountID || obs.NativeID != input.Account.NativeID || obs.Model != input.Model ||
		len(input.Channels) != 1 || obs.ActualChannel != input.Channels[0] || !obs.IdentityConfirmed || !c.ValidRequestID(obs.RequestID) ||
		obs.UpstreamStatus < 200 || obs.UpstreamStatus >= 300 {
		return false
	}
	if input.Mode == c.VerificationConnection {
		return true
	}
	return obs.Finished && strings.IndexFunc(obs.VisibleText, func(r rune) bool { return !unicode.IsSpace(r) && !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) }) >= 0
}

func verificationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return contextError(ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextError(err)
	}
	var typed *c.Error
	if errors.As(err, &typed) && (typed.Code == c.CodeVersionConflict || typed.Code == c.CodeInvalidState || typed.Code == c.CodeInvalidCredentials || typed.Code == c.CodeNotFound) {
		return c.NewError(typed.Code)
	}
	return c.NewError(c.CodeVerificationFailed)
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return c.NewError(c.CodeVerificationTimeout)
	}
	return c.NewError(c.CodeCancelled)
}

func (v *verifier) Close(ctx context.Context) error {
	v.mu.Lock()
	v.closed = true
	for op := range v.active {
		op.cancel()
	}
	if len(v.active) == 0 {
		select {
		case <-v.done:
		default:
			close(v.done)
		}
	}
	v.mu.Unlock()
	select {
	case <-v.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
