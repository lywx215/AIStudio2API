package management

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementverify"
)

type channelTaskTarget struct {
	failPlayground    bool
	calls             []c.Channel
	playgroundEntered chan struct{}
}

func (t *channelTaskTarget) Open(_ context.Context, in c.VerificationInput) (c.VerificationSession, error) {
	return &channelTaskSession{target: t, input: in}, nil
}

type channelTaskSession struct {
	target *channelTaskTarget
	input  c.VerificationInput
}

func (s *channelTaskSession) Close() error { return nil }
func (s *channelTaskSession) Run(ctx context.Context, p c.VerificationProbe, _ c.ProgressFunc) (c.VerificationObservation, error) {
	s.target.calls = append(s.target.calls, p.Channel)
	o := c.VerificationObservation{AccountID: s.input.Account.AccountID, NativeID: s.input.Account.NativeID, Model: p.Model, ActualChannel: p.Channel, RequestID: fixtureRequestID, UpstreamStatus: 200, VisibleText: "OK", Finished: true, IdentityConfirmed: true}
	if p.Channel == c.ChannelPlayground && s.target.playgroundEntered != nil {
		close(s.target.playgroundEntered)
		<-ctx.Done()
		return o, ctx.Err()
	}
	if s.target.failPlayground && p.Channel == c.ChannelPlayground {
		o.UpstreamStatus = 403
		o.VisibleText = ""
	}
	return o, nil
}

func TestVerificationCheckpointPublishesTrustedIndexWhileSuccessorPending(t *testing.T) {
	store := newMemoryStore()
	seedAccount(store, true)
	account, _ := store.GetAccount(context.Background(), fixtureAccountID)
	target := &channelTaskTarget{playgroundEntered: make(chan struct{})}
	verifier, _ := managementverify.New(c.VerifierOptions{Target: target})
	service, err := NewTaskService(c.TaskServiceOptions{Store: store, Accounts: &fakeAccounts{store: store}, Verifier: verifier, Clock: fixtureTime})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	accepted, err := service.Submit(context.Background(), admission(t, c.TaskTest, "pending-index", taskInput{Model: c.DefaultModel, Action: "model", Channels: []c.Channel{c.ChannelBuild, c.ChannelPlayground}}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-target.playgroundEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Playground did not enter pending state")
	}
	r, err := store.GetTask(context.Background(), accepted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	item := r.Items[0]
	if r.Status != c.TaskRunning || item.Status != c.TaskRunning || item.Index == nil || *item.Index != account.Index || item.Result == nil || item.Result.Verification == nil {
		t.Fatalf("pending checkpoint lost attribution: %+v", r)
	}
	v := item.Result.Verification
	if v.Success || len(v.ChannelResults) != 1 || !v.ChannelResults[0].Success || v.ChannelResults[0].AuthIndex != *item.Index || v.ChannelResults[0].RequestedChannel != c.ChannelBuild {
		t.Fatalf("pending child not attributable: %+v", v)
	}
	if _, err = service.Cancel(context.Background(), accepted.TaskID, c.Principal{KeyID: fixtureKeyID, Scopes: c.Scopes()}); err != nil {
		t.Fatal(err)
	}
	terminal := awaitTerminal(t, store, accepted.TaskID)
	if terminal.Status != c.TaskCancelled || terminal.Items[0].Index == nil || *terminal.Items[0].Index != account.Index || !terminal.Items[0].Result.Verification.ChannelResults[0].Success {
		t.Fatalf("cancel lost checkpoint: %+v", terminal)
	}
}

func TestVerificationCheckpointRequiresExistingIndexConsistency(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-index", true: "conflicting-index"}[mismatch], func(t *testing.T) {
			store := newMemoryStore()
			seedAccount(store, true)
			account, _ := store.GetAccount(context.Background(), fixtureAccountID)
			a := admission(t, c.TaskTest, "existing-index", taskInput{Model: c.DefaultModel, Action: "model", Channels: []c.Channel{c.ChannelBuild}})
			a.QueueLimit = 100
			admitted, err := store.AdmitTask(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			work, err := store.ClaimTask(context.Background(), admitted.Accepted.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			r := work.Record
			value := account.Index
			if mismatch {
				value++
			}
			r.Items[0].Index = &value
			before, err := store.SaveTask(context.Background(), r, r.Revision)
			if err != nil {
				t.Fatal(err)
			}
			verifier, _ := managementverify.New(c.VerifierOptions{Target: &channelTaskTarget{}})
			s := &taskService{store: store, verifier: verifier, clock: fixtureTime}
			_, err = s.verify(context.Background(), c.VerificationInput{Account: account, Model: c.DefaultModel, Mode: c.VerificationModel, Channels: []c.Channel{c.ChannelBuild}}, nil, r.TaskID, 0)
			after, _ := store.GetTask(context.Background(), r.TaskID)
			if mismatch {
				if err == nil || after.Revision != before.Revision || after.Items[0].Result != nil || *after.Items[0].Index != value {
					t.Fatalf("conflicting index overwritten: %+v err=%v", after, err)
				}
			} else if err != nil || after.Items[0].Result == nil || *after.Items[0].Index != account.Index {
				t.Fatalf("matching index rejected: %+v err=%v", after, err)
			}
		})
	}
}
func (s *channelTaskSession) RefreshedCandidate(context.Context) (*c.CredentialMaterial, error) {
	return &c.CredentialMaterial{NativeID: s.input.Account.NativeID, StorageState: []byte(`{"synthetic":"private-cookie-refresh"}`)}, nil
}

type channelTaskAccounts struct {
	*fakeAccounts
	conflict      bool
	candidateSeen bool
}

func (a *channelTaskAccounts) Replace(ctx context.Context, cmd c.ReplaceCommand) (c.AccountCommitResult, error) {
	a.candidateSeen = cmd.Candidate != nil && strings.Contains(string(cmd.Candidate.StorageState), "private-cookie-refresh")
	if a.conflict {
		a.store.mu.Lock()
		live := a.store.accounts[cmd.AccountID]
		live.StateVersion++
		a.store.accounts[cmd.AccountID] = live
		a.store.mu.Unlock()
	}
	return a.fakeAccounts.Replace(ctx, cmd)
}

func TestReplacementBothChannelsCandidateAndCommitEvidence(t *testing.T) {
	for _, scenario := range []string{"success", "playground-fails", "commit-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			store := newMemoryStore()
			seedAccount(store, false)
			accounts := &channelTaskAccounts{fakeAccounts: &fakeAccounts{store: store}, conflict: scenario == "commit-conflict"}
			target := &channelTaskTarget{failPlayground: scenario == "playground-fails"}
			verifier, _ := managementverify.New(c.VerifierOptions{Target: target})
			service, err := NewTaskService(c.TaskServiceOptions{Store: store, Accounts: accounts, Verifier: verifier, Clock: fixtureTime})
			if err != nil {
				t.Fatal(err)
			}
			if err = service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := service.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			cv, sv := uint64(5), uint64(6)
			a := admission(t, c.TaskReplace, scenario, taskInput{Model: c.DefaultModel, Verify: true, Channels: []c.Channel{c.ChannelBuild, c.ChannelPlayground}, Expected: c.ExpectedVersions{Credential: &cv, State: &sv}, Credentials: []c.Credentials{fixtureCredentials()}})
			accepted, err := service.Submit(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			r := awaitTerminal(t, store, accepted.TaskID)
			if r.Items[0].Result == nil || r.Items[0].Result.Verification == nil {
				t.Fatalf("lost verification evidence: %+v", r)
			}
			v := r.Items[0].Result.Verification
			if len(v.ChannelResults) != 2 || len(target.calls) != 2 || !v.ChannelResults[0].Success || !c.ValidVerificationResult(*v) {
				t.Fatalf("invalid channel evidence: %+v", v)
			}
			if scenario != "success" && v.Versions != (c.Versions{CredentialVersion: 5, StateVersion: 6}) {
				t.Fatal("historical version replaced by later commit")
			}
			for _, child := range v.ChannelResults {
				if child.Versions != v.Versions {
					t.Fatal("mixed parent/child versions")
				}
			}
			encoded, _ := json.Marshal(r.Task)
			if strings.Contains(string(encoded), "private-cookie-refresh") {
				t.Fatal("private candidate escaped")
			}
			switch scenario {
			case "success":
				if r.Status != c.TaskSucceeded || !v.Success || r.Items[0].Upload == nil || r.Items[0].Upload.CredentialVersion != 6 || !accounts.candidateSeen || v.Versions != r.Items[0].Upload.Versions {
					t.Fatalf("bad commit: %+v", r)
				}
			case "playground-fails":
				if r.Status != c.TaskFailed || v.Success || v.ChannelResults[1].Success || accounts.replaces != 0 || r.Items[0].Upload != nil {
					t.Fatalf("failed verification committed: %+v", r)
				}
			case "commit-conflict":
				if r.Status != c.TaskFailed || r.Items[0].Error.Code != c.CodeVersionConflict || v.Success || !v.ChannelResults[1].Success || r.Items[0].Upload != nil || !accounts.candidateSeen {
					t.Fatalf("CAS conflict lost evidence: %+v", r)
				}
			}
		})
	}
}
