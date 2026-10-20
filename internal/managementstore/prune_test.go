package managementstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func tableCount(t *testing.T, s *store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPruneLargeSnapshotBatchesCancellationAndProtectedEvidence(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	seedAccounts(t, s, 405)
	key := createTestKey(t, s)
	reload := admitTarget(t, s, key, "old-large-reload", c.TaskReloadAuth, "", c.ExpectedVersions{})
	if _, err := s.RequestTaskCancellation(ctx, reload.Accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	for i := range 107 {
		r := admitTest(t, s, key, fmt.Sprintf("old-%03d", i), c.TaskImport)
		if _, err := s.RequestTaskCancellation(ctx, r.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	committedTask := admitTest(t, s, key, "old-committed", c.TaskImport)
	upload := &c.UploadIdentity{TaskID: committedTask.TaskID, ItemIndex: 0, Kind: c.TaskImport}
	committed := importTest(t, s, newParticipant(t), "prune-proof@example.invalid", upload)
	if _, err := s.RequestTaskCancellation(ctx, committedTask.TaskID); err != nil {
		t.Fatal(err)
	}
	runningTask := admitTest(t, s, key, "old-still-running", c.TaskImport)
	if _, err := s.ClaimTask(ctx, runningTask.TaskID); err != nil {
		t.Fatal(err)
	}
	for range 107 {
		if err := s.AppendAudit(ctx, c.AuditEntry{AuditID: id(t, "audit"), RequestID: id(t, "req"), CreatedByKeyID: key.Key.ID, Action: "import", Timestamp: s.now(), Outcome: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	identities, receipts, journals := tableCount(t, s, "identities"), tableCount(t, s, "upload_receipts"), tableCount(t, s, "account_commits")
	s.clock = func() time.Time { return testTime.Add(31 * 24 * time.Hour) }
	recent := admitTest(t, s, key, "recent-queued", c.TaskImport)
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	first := true
	s.fault = func(point string) error {
		if point == "prune-preconditions" && first {
			first = false
			cancel()
		}
		return nil
	}
	err := s.Prune(cancelled, s.now())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if remaining := tableCount(t, s, "task_preconditions"); remaining != 405-c.PruneBatchRows {
		t.Fatal("child cleanup was not one durable bounded batch", remaining)
	}
	if _, err = s.GetTask(ctx, reload.Accepted.TaskID); err != nil {
		t.Fatal("large task deleted with remaining children", err)
	}
	before := map[string]int{"prune-preconditions": tableCount(t, s, "task_preconditions"), "prune-tasks": tableCount(t, s, "tasks"), "prune-inputs": tableCount(t, s, "task_inputs"), "prune-audit": tableCount(t, s, "audit")}
	tables := map[string]string{"prune-preconditions": "task_preconditions", "prune-tasks": "tasks", "prune-inputs": "task_inputs", "prune-audit": "audit"}
	calls := 0
	s.fault = func(point string) error {
		table, exists := tables[point]
		if !exists {
			return nil
		}
		now := tableCount(t, s, table)
		removed := before[point] - now
		if removed < 0 || removed > c.PruneBatchRows {
			t.Fatalf("%s removed %d rows in one batch", point, removed)
		}
		before[point] = now
		calls++
		return nil
	}
	if err = s.Prune(ctx, s.now()); err != nil {
		t.Fatal(err)
	}
	s.fault = nil
	if calls < 8 {
		t.Fatal("did not exercise multiple short transactions", calls)
	}
	if tableCount(t, s, "task_preconditions") != 0 || tableCount(t, s, "task_precondition_sets") != 2 || tableCount(t, s, "tasks") != 2 || tableCount(t, s, "idempotency") != 2 || tableCount(t, s, "audit") != 0 {
		t.Fatal("retention left terminal history or removed active work")
	}
	for _, task := range []c.TaskRecord{runningTask, recent} {
		r, err := s.GetTask(ctx, task.TaskID)
		if err != nil || r.Status.Terminal() {
			t.Fatal(r, err)
		}
	}
	if tableCount(t, s, "task_inputs") != 2 {
		t.Fatal("running/queued input pruned")
	}
	if tableCount(t, s, "identities") != identities || tableCount(t, s, "upload_receipts") != receipts || tableCount(t, s, "account_commits") != journals {
		t.Fatal("durable evidence pruned")
	}
	receipt, err := s.GetUploadReceipt(ctx, *upload)
	if err != nil || !reflect.DeepEqual(receipt, committed.Upload) {
		t.Fatal(receipt, err)
	}
	// An already-cancelled run does no additional SQL mutation.
	stopped, stop := context.WithCancel(ctx)
	stop()
	err = s.Prune(stopped, s.now())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
