package managementstore

import (
	"context"
	"path/filepath"
	"testing"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestCompletedChannelCheckpointSurvivesRestartWithoutReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "channels.db")
	s := openTest(t, path)
	key := createTestKey(t, s)
	account := seedAccounts(t, s, 1)[0]
	admitted := admitTarget(t, s, key, "dual-checkpoint", c.TaskTest, account.AccountID, c.ExpectedVersions{})
	work, err := s.ClaimTask(ctx, admitted.Accepted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	r := work.Record
	status := 200
	r.Items[0].Status = c.TaskRunning
	r.Items[0].Stage = "verifying"
	r.Items[0].Progress = 45
	trustedIndex := account.Index
	r.Items[0].Index = &trustedIndex
	r.Items[0].Result = &c.ItemResult{Verification: &c.VerificationResult{AuthIndex: account.Index, Model: c.DefaultModel, Stage: "verifying", Versions: account.Versions, Channels: []c.Channel{c.ChannelBuild, c.ChannelPlayground}, ChannelResults: []c.VerificationChannelResult{{RequestedChannel: c.ChannelBuild, ActualChannel: c.ChannelBuild, Success: true, AuthIndex: account.Index, Model: c.DefaultModel, RequestID: id(t, "req"), UpstreamStatus: &status, Stage: "model_verified", Versions: account.Versions}}}}
	if _, err = s.SaveTask(ctx, r, r.Revision); err != nil {
		t.Fatal(err)
	}
	pending, err := s.GetTask(ctx, r.TaskID)
	if err != nil || pending.Items[0].Index == nil || *pending.Items[0].Index != account.Index || pending.Items[0].Result.Verification.ChannelResults[0].AuthIndex != *pending.Items[0].Index {
		t.Fatalf("durable checkpoint lost index: %+v %v", pending, err)
	}
	conflicting := pending
	conflicting.Items = append([]c.TaskItem{}, pending.Items...)
	wrongIndex := account.Index + 1
	conflicting.Items[0].Index = &wrongIndex
	if _, err = s.SaveTask(ctx, conflicting, conflicting.Revision); c.PublicError(err).Code != c.CodeInvalidState {
		t.Fatalf("existing index changed: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	queued, err := s.RecoverTasks(ctx)
	if err != nil || len(queued) != 0 {
		t.Fatalf("replayed running task: %v %v", queued, err)
	}
	recovered, err := s.GetTask(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	v := recovered.Items[0].Result.Verification
	if recovered.Status != c.TaskInterrupted || recovered.Items[0].Index == nil || *recovered.Items[0].Index != account.Index || v.Success || len(v.ChannelResults) != 1 || !v.ChannelResults[0].Success || v.ChannelResults[0].RequestedChannel != c.ChannelBuild || v.ChannelResults[0].AuthIndex != *recovered.Items[0].Index {
		t.Fatalf("checkpoint lost: %+v", recovered)
	}
	if _, err = s.ClaimTask(ctx, r.TaskID); err == nil {
		t.Fatal("interrupted generation eligible for replay")
	}
}
