// Package managementverify validates evidence from an isolated, pinned target.
// It never publishes credentials or changes account state.
package managementverify

import (
	"context"
	"errors"
	"fmt"
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
	session, openErr := v.target.Open(ctx, input)
	if openErr != nil {
		// A target may have allocated a partial session before failing.
		if session != nil {
			_ = session.Close()
		}
		return out, verificationError(ctx, openErr)
	}
	if session == nil {
		return out, c.NewError(c.CodeVerificationFailed)
	}
	var closeOnce sync.Once
	var closeErr error
	closed := make(chan struct{})
	closeSession := func() {
		closeOnce.Do(func() { closeErr = session.Close(); close(closed) })
	}
	stop := context.AfterFunc(ctx, closeSession)
	defer func() {
		stop()
		closeSession()
		<-closed
		if err == nil && closeErr != nil {
			out = c.VerificationOutput{}
			err = c.NewError(c.CodeVerificationFailed)
		}
		if ctx.Err() != nil {
			out = c.VerificationOutput{}
			err = contextError(ctx.Err())
		}
	}()
	observation, runErr := session.Run(ctx, c.VerificationProbe{
		Mode: input.Mode, Model: input.Model, Prompt: ProbePrompt, MaxOutputTokens: ProbeMaxOutputTokens,
	}, progress)
	if runErr != nil {
		return out, verificationError(ctx, runErr)
	}
	if err = ctx.Err(); err != nil {
		return out, contextError(err)
	}
	if !validObservation(input, observation) {
		return out, c.NewError(c.CodeVerificationFailed)
	}
	stage := "model_verified"
	if input.Mode == c.VerificationConnection {
		stage = "connection_verified"
	}
	status := observation.UpstreamStatus
	out.Result = c.VerificationResult{
		Success: true, AuthIndex: input.Account.Index, Model: input.Model,
		RequestID: observation.RequestID, UpstreamStatus: &status, Stage: stage,
		Versions: input.Account.Versions, ActualChannel: observation.ActualChannel,
	}
	if input.Candidate != nil {
		if source, ok := session.(CandidateSource); ok {
			candidate, candidateErr := source.RefreshedCandidate(ctx)
			if candidateErr != nil {
				return c.VerificationOutput{}, verificationError(ctx, candidateErr)
			}
			if candidate != nil {
				if candidate.NativeID != input.Account.NativeID || len(candidate.StorageState) == 0 || len(candidate.StorageState) > c.MaxCredentialBytes {
					return c.VerificationOutput{}, c.NewError(c.CodeVerificationFailed)
				}
				material := *candidate
				material.StorageState = append([]byte(nil), material.StorageState...)
				out.RefreshedCandidate = &material
			}
		}
	}
	if progress != nil {
		progress(stage, 100)
	}
	return out, nil
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
		obs.ActualChannel != c.ChannelPlayground || !obs.IdentityConfirmed || !c.ValidRequestID(obs.RequestID) ||
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
