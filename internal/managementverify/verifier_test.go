package managementverify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

const accountID = "22de91c7-c700-453c-af76-16693460fa63"
const requestID = "req_dfcfb928-d90b-4a81-a23e-bc5ed7eecab7"

func inputFixture() c.VerificationInput {
	return c.VerificationInput{Account: c.AccountRecord{Account: c.Account{AccountID: accountID, Index: 7, Versions: c.Versions{CredentialVersion: 3, StateVersion: 5}}, NativeID: "synthetic@example.test"}, Model: "synthetic-model", Mode: c.VerificationModel, Channels: []c.Channel{c.ChannelPlayground}}
}

func observationFixture() c.VerificationObservation {
	return c.VerificationObservation{AccountID: accountID, NativeID: "synthetic@example.test", Model: "synthetic-model", RequestID: requestID, UpstreamStatus: 200, ActualChannel: c.ChannelPlayground, VisibleText: "OK", Finished: true, IdentityConfirmed: true}
}

type targetFunc func(context.Context, c.VerificationInput) (c.VerificationSession, error)

func (f targetFunc) Open(ctx context.Context, input c.VerificationInput) (c.VerificationSession, error) {
	return f(ctx, input)
}

type fakeSession struct {
	run       func(context.Context, c.VerificationProbe) (c.VerificationObservation, error)
	closed    atomic.Int32
	candidate *c.CredentialMaterial
	close     func()
}

func (s *fakeSession) Run(ctx context.Context, p c.VerificationProbe, _ c.ProgressFunc) (c.VerificationObservation, error) {
	return s.run(ctx, p)
}
func (s *fakeSession) Close() error {
	s.closed.Add(1)
	if s.close != nil {
		s.close()
	}
	return nil
}
func (s *fakeSession) RefreshedCandidate(context.Context) (*c.CredentialMaterial, error) {
	return s.candidate, nil
}

func errorCode(t *testing.T, err error, want c.ErrorCode) {
	t.Helper()
	var typed *c.Error
	if !errors.As(err, &typed) || typed.Code != want {
		t.Fatalf("error code=%v want=%s", err, want)
	}
}

func TestVerifierEvidence(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*c.VerificationObservation)
		mode    c.VerificationMode
		success bool
	}{
		{"model", func(*c.VerificationObservation) {}, c.VerificationModel, true},
		{"empty", func(o *c.VerificationObservation) { o.VisibleText = "" }, c.VerificationModel, false},
		{"thought-only", func(o *c.VerificationObservation) { o.VisibleText = " \n\t" }, c.VerificationModel, false},
		{"invisible", func(o *c.VerificationObservation) { o.VisibleText = "\u200b\x00" }, c.VerificationModel, false},
		{"missing-terminal", func(o *c.VerificationObservation) { o.Finished = false }, c.VerificationModel, false},
		{"wrong-account", func(o *c.VerificationObservation) { o.AccountID = "bc301edf-dacd-4a4b-a9d9-8897b2000d84" }, c.VerificationModel, false},
		{"wrong-native", func(o *c.VerificationObservation) { o.NativeID = "other@example.test" }, c.VerificationModel, false},
		{"wrong-model", func(o *c.VerificationObservation) { o.Model = "different-model" }, c.VerificationModel, false},
		{"build", func(o *c.VerificationObservation) { o.ActualChannel = "build" }, c.VerificationModel, false},
		{"no-identity", func(o *c.VerificationObservation) { o.IdentityConfirmed = false }, c.VerificationModel, false},
		{"http-error", func(o *c.VerificationObservation) { o.UpstreamStatus = 403 }, c.VerificationModel, false},
		{"request-id", func(o *c.VerificationObservation) { o.RequestID = "req_not-a-uuid" }, c.VerificationModel, false},
		{"connection", func(o *c.VerificationObservation) { o.VisibleText = ""; o.Finished = false }, c.VerificationConnection, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := observationFixture()
			tc.change(&obs)
			session := &fakeSession{run: func(_ context.Context, probe c.VerificationProbe) (c.VerificationObservation, error) {
				if probe.Prompt != ProbePrompt || probe.MaxOutputTokens != ProbeMaxOutputTokens {
					t.Fatal("unbounded probe")
				}
				return obs, nil
			}}
			v, _ := New(c.VerifierOptions{Target: targetFunc(func(context.Context, c.VerificationInput) (c.VerificationSession, error) { return session, nil })})
			input := inputFixture()
			input.Mode = tc.mode
			out, err := v.Verify(context.Background(), input, nil)
			if tc.success {
				if err != nil || !out.Result.Success || out.Result.Versions != input.Account.Versions {
					t.Fatalf("result=%+v error=%v", out.Result, err)
				}
				if tc.mode == c.VerificationConnection && out.Result.Stage != "connection_verified" {
					t.Fatal("connection upgraded to model evidence")
				}
			} else {
				errorCode(t, err, c.CodeVerificationFailed)
				if out.Result.Success {
					t.Fatal("failure returned successful evidence")
				}
			}
			if session.closed.Load() != 1 {
				t.Fatal("session not closed exactly once")
			}
		})
	}
}

func TestVerifierIsolationAndHistoricalVersions(t *testing.T) {
	input := inputFixture()
	input.Candidate = &c.CredentialMaterial{NativeID: input.Account.NativeID, StorageState: []byte(`{"cookies":[],"origins":[]}`)}
	original := string(input.Candidate.StorageState)
	refreshed := &c.CredentialMaterial{NativeID: input.Account.NativeID, StorageState: []byte(`{"cookies":["private"]}`)}
	v, _ := New(c.VerifierOptions{Target: targetFunc(func(_ context.Context, copy c.VerificationInput) (c.VerificationSession, error) {
		copy.Candidate.StorageState[0] = ' '
		copy.Account.StateVersion++
		return &fakeSession{candidate: refreshed, run: func(context.Context, c.VerificationProbe) (c.VerificationObservation, error) {
			return observationFixture(), nil
		}}, nil
	})})
	out, err := v.Verify(context.Background(), input, nil)
	if err != nil || out.Result.StateVersion != 5 || string(input.Candidate.StorageState) != original || out.RefreshedCandidate == nil {
		t.Fatalf("isolation or historical version failed: %v", err)
	}
	out.RefreshedCandidate.StorageState[0] = ' '
	if refreshed.StorageState[0] != '{' {
		t.Fatal("refreshed bytes shared")
	}
	if _, err := out.RefreshedCandidate.MarshalJSON(); err == nil {
		t.Fatal("private credentials serializable")
	}
}

func TestVerifierDeadlineCancellationAndClose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
		want   c.ErrorCode
	}{{"deadline", context.DeadlineExceeded, c.CodeVerificationTimeout}, {"cancel", context.Canceled, c.CodeCancelled}, {"redacted", errors.New("secret-cookie-value"), c.CodeVerificationFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeSession{run: func(context.Context, c.VerificationProbe) (c.VerificationObservation, error) {
				return c.VerificationObservation{}, tc.runErr
			}}
			v, _ := New(c.VerifierOptions{Target: targetFunc(func(context.Context, c.VerificationInput) (c.VerificationSession, error) { return session, nil })})
			_, err := v.Verify(context.Background(), inputFixture(), nil)
			errorCode(t, err, tc.want)
			if strings.Contains(err.Error(), "secret") || session.closed.Load() != 1 {
				t.Fatal("secret leak or resource leak")
			}
		})
	}
	opened := make(chan struct{})
	released := make(chan struct{})
	session := &fakeSession{run: func(ctx context.Context, _ c.VerificationProbe) (c.VerificationObservation, error) {
		close(opened)
		<-ctx.Done()
		return c.VerificationObservation{}, ctx.Err()
	}, close: func() { close(released) }}
	v, _ := New(c.VerifierOptions{Target: targetFunc(func(ctx context.Context, _ c.VerificationInput) (c.VerificationSession, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > c.VerificationTimeout {
			t.Error("missing bounded deadline")
		}
		return session, nil
	})})
	done := make(chan error, 1)
	go func() { _, err := v.Verify(context.Background(), inputFixture(), nil); done <- err }()
	<-opened
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := v.Close(ctx); err != nil {
		t.Fatal(err)
	}
	errorCode(t, <-done, c.CodeCancelled)
	select {
	case <-released:
	default:
		t.Fatal("Close returned before release")
	}
	if session.closed.Load() != 1 {
		t.Fatal("duplicate close")
	}
	_, err := v.Verify(context.Background(), inputFixture(), nil)
	errorCode(t, err, c.CodeInterrupted)
}

func TestVerifierConcurrencyAndQueuedCancel(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var count atomic.Int32
	v, _ := New(c.VerifierOptions{Target: targetFunc(func(context.Context, c.VerificationInput) (c.VerificationSession, error) {
		count.Add(1)
		return &fakeSession{run: func(ctx context.Context, _ c.VerificationProbe) (c.VerificationObservation, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return observationFixture(), nil
			case <-ctx.Done():
				return c.VerificationObservation{}, ctx.Err()
			}
		}}, nil
	})})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = v.Verify(context.Background(), inputFixture(), nil) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := v.Verify(ctx, inputFixture(), nil)
	errorCode(t, err, c.CodeCancelled)
	if count.Load() != 1 {
		t.Fatal("queued cancelled request opened target")
	}
	close(release)
	wg.Wait()
	if err := v.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVerifierBothAndFinalCheckpointFailureRetainsEvidence(t *testing.T) {
	for _, code := range []c.ErrorCode{c.CodePersistenceError, c.CodeVersionConflict} {
		t.Run(string(code), func(t *testing.T) {
			input := inputFixture()
			input.Channels = []c.Channel{c.ChannelPlayground, c.ChannelBuild}
			var opened []c.Channel
			input.Checkpoint = func(result c.VerificationResult) error {
				if len(result.ChannelResults) == 2 {
					return c.NewError(code)
				}
				return nil
			}
			v, _ := New(c.VerifierOptions{Target: targetFunc(func(_ context.Context, in c.VerificationInput) (c.VerificationSession, error) {
				channel := in.Channels[0]
				opened = append(opened, channel)
				return &fakeSession{run: func(_ context.Context, probe c.VerificationProbe) (c.VerificationObservation, error) {
					obs := observationFixture()
					obs.ActualChannel = channel
					if probe.Channel != channel {
						t.Fatal("probe channel changed")
					}
					return obs, nil
				}}, nil
			})})
			out, err := v.Verify(context.Background(), input, nil)
			errorCode(t, err, code)
			if out.Result.Success || !c.ValidVerificationResult(out.Result) || len(opened) != 2 || opened[0] != c.ChannelBuild || opened[1] != c.ChannelPlayground || len(out.Result.ChannelResults) != 2 || !out.Result.ChannelResults[0].Success || !out.Result.ChannelResults[1].Success {
				t.Fatalf("completed evidence lost: %+v", out.Result)
			}
		})
	}
}
