package managementstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func seedAccounts(t *testing.T, s *store, n int) []c.Account {
	t.Helper()
	seeds := make([]c.NativeAccount, n)
	for i := range seeds {
		native := fmt.Sprintf("snapshot-%04d@example.invalid", i)
		seeds[i] = c.NativeAccount{NativeID: native, State: c.AccountState{Enabled: i%2 == 0}, CredentialDigest: sha256.Sum256([]byte(native))}
	}
	if err := s.ReconcileAccounts(context.Background(), seeds); err != nil {
		t.Fatal(err)
	}
	result := []c.Account{}
	for offset := 0; ; {
		page, err := s.ListAccounts(context.Background(), c.PageQuery{Offset: offset, Limit: c.MaxPageLimit})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.Items...)
		offset += len(page.Items)
		if offset >= page.Total {
			break
		}
	}
	return result
}
func admitTarget(t *testing.T, s *store, key c.KeyCreated, idem string, kind c.TaskKind, account string, exp c.ExpectedVersions) c.AdmittedTask {
	t.Helper()
	a := testAdmission(key, idem, kind)
	a.Items[0].AccountID = account
	a.Capture.Expected = exp
	r, err := s.AdmitTask(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdmissionSnapshotReplayAndClientExpectedNeverRebase(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	accounts := seedAccounts(t, s, 1)
	key := createTestKey(t, s)
	a := testAdmission(key, "snapshot", c.TaskReplace)
	a.Items[0].AccountID = accounts[0].AccountID
	futureCredential, futureState := uint64(2), uint64(1)
	a.Capture.Expected = c.ExpectedVersions{Credential: &futureCredential, State: &futureState}
	originalBody := append([]byte{}, a.PrivateInput...)
	originalDigest := a.CanonicalDigest
	admitted, err := s.AdmitTask(ctx, a)
	if err != nil {
		t.Fatal("explicit CAS mismatch must be asynchronous", err)
	}
	page, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Limit: 50})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	p := page.Items[0]
	if !p.SnapshotKnown || p.Missing || p.Versions != accounts[0].Versions || p.ClientExpected.Credential == nil || *p.ClientExpected.Credential != 2 {
		t.Fatal(p)
	}
	current, err := s.GetAccount(ctx, accounts[0].AccountID)
	if err != nil {
		t.Fatal(err)
	}
	assertCode(t, p.Check(current), c.CodeVersionConflict)
	participant := newParticipant(t)
	changed, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: current.AccountID, Expected: expected(current), CredentialDigest: sha256.Sum256([]byte("new synthetic lineage"))}, participant)
	if err != nil {
		t.Fatal(err)
	}
	// A later version that equals the client's formerly-future expectation must
	// still fail against the independently retained admission snapshot.
	if changed.Account.CredentialVersion != 2 {
		t.Fatal(changed.Account.Versions)
	}
	assertCode(t, p.Check(changed.Account), c.CodeVersionConflict)
	a.QueueLimit = 1
	a.Capture = c.TaskCaptureInstruction{}
	replay, err := s.AdmitTask(ctx, a)
	if err != nil || !replay.Replayed || replay.Accepted != admitted.Accepted {
		t.Fatal(replay, err)
	}
	page, err = s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Limit: 50})
	if err != nil || !reflect.DeepEqual(page.Items[0], p) {
		t.Fatal("replay recaptured versions", page, err)
	}
	var body, digest []byte
	if s.db.QueryRow("SELECT input FROM task_inputs WHERE task_id=?", admitted.Accepted.TaskID).Scan(&body) != nil || !reflect.DeepEqual(body, originalBody) {
		t.Fatal("private body rewritten")
	}
	if s.db.QueryRow("SELECT digest FROM idempotency WHERE task_id=?", admitted.Accepted.TaskID).Scan(&digest) != nil || !reflect.DeepEqual(digest, originalDigest[:]) {
		t.Fatal("digest changed")
	}
	work, err := s.ClaimTask(ctx, admitted.Accepted.TaskID)
	if err != nil || work.PreconditionSet == nil || work.PreconditionSet.TargetCount != 1 || !reflect.DeepEqual(work.Preconditions, []c.TaskPrecondition{p}) {
		t.Fatal(work, err)
	}
	work.Preconditions[0].ClientExpected.Credential = new(uint64)
	detached, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Limit: 1})
	if err != nil || *detached.Items[0].ClientExpected.Credential != 2 {
		t.Fatal("snapshot aliases storage", err)
	}
	if _, err = s.db.Exec("UPDATE task_preconditions SET missing=1 WHERE task_id=?", admitted.Accepted.TaskID); err == nil {
		t.Fatal("mutable precondition")
	}
	if _, err = s.db.Exec("UPDATE task_precondition_sets SET target_count=2 WHERE task_id=?", admitted.Accepted.TaskID); err == nil {
		t.Fatal("mutable manifest")
	}
}

func TestAdmissionMissingMarkerDoesNotBindFutureAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	target := id(t, "")
	admitted := admitTarget(t, s, key, "missing", c.TaskTest, target, c.ExpectedVersions{})
	page, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Limit: 1})
	if err != nil || !page.Items[0].Missing || !page.Items[0].SnapshotKnown || page.Items[0].Versions != (c.Versions{}) {
		t.Fatal(page, err)
	}
	fakeFuture := c.AccountRecord{Account: c.Account{AccountID: target, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}}}
	assertCode(t, page.Items[0].Check(fakeFuture), c.CodeNotFound)
	work, err := s.ClaimTask(ctx, admitted.Accepted.TaskID)
	if err != nil || !work.Preconditions[0].Missing {
		t.Fatal(work, err)
	}
}

func TestAdmissionAndAccountCommitUseOneDatabaseSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := openTest(t, "")
	p := newParticipant(t)
	initial := importTest(t, s, p, "concurrent-snapshot@example.invalid", nil)
	key := createTestKey(t, s)
	preparing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.probe = func(ctx context.Context) error {
		once.Do(func() {
			close(preparing)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		return ctx.Err()
	}
	committed := make(chan error, 1)
	go func() {
		_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: mustID("op"), Kind: c.MutationReplace, AccountID: initial.Account.AccountID, Expected: expected(initial.Account), CredentialDigest: sha256.Sum256([]byte("newer synthetic snapshot"))}, p)
		committed <- err
	}()
	select {
	case <-preparing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	a := testAdmission(key, "during-prepare", c.TaskTest)
	a.Items[0].AccountID = initial.Account.AccountID
	admitted, err := s.AdmitTask(ctx, a)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-committed; err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Limit: 1})
	if err != nil || page.Items[0].CredentialVersion != 1 {
		t.Fatal(page, err)
	}
	current, err := s.GetAccount(ctx, initial.Account.AccountID)
	if err != nil || current.CredentialVersion != 2 {
		t.Fatal(current, err)
	}
	assertCode(t, page.Items[0].Check(current), c.CodeVersionConflict)
}

func TestReloadAuthCapturesFixedPagedAndEmptySets(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		s := openTest(t, "")
		key := createTestKey(t, s)
		admitted := admitTarget(t, s, key, "empty-reload", c.TaskReloadAuth, "", c.ExpectedVersions{})
		work, err := s.ClaimTask(context.Background(), admitted.Accepted.TaskID)
		if err != nil || work.PreconditionSet == nil || work.PreconditionSet.TargetCount != 0 || work.PreconditionSet.Mode != c.TaskCaptureReloadAuth || work.Preconditions == nil || len(work.Preconditions) != 0 {
			t.Fatal(work, err)
		}
	})
	t.Run("more_than_200", func(t *testing.T) {
		ctx := context.Background()
		s := openTest(t, "")
		accounts := seedAccounts(t, s, 405)
		key := createTestKey(t, s)
		archived, err := s.GetAccount(ctx, accounts[0].AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationArchive, AccountID: archived.AccountID}, newParticipant(t)); err != nil {
			t.Fatal(err)
		}
		admitted := admitTarget(t, s, key, "paged-reload", c.TaskReloadAuth, "", c.ExpectedVersions{})
		// A later addition is never incorporated into this immutable set.
		if err = s.ReconcileAccounts(ctx, []c.NativeAccount{{NativeID: "later@example.invalid", State: c.AccountState{Enabled: true}, CredentialDigest: sha256.Sum256([]byte("later"))}}); err != nil {
			t.Fatal(err)
		}
		live, err := s.GetAccount(ctx, accounts[1].AccountID)
		if err != nil {
			t.Fatal(err)
		}
		state := live.AccountState
		state.Enabled = !state.Enabled
		if _, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationState, AccountID: live.AccountID, State: &state}, newParticipant(t)); err != nil {
			t.Fatal(err)
		}
		work, err := s.ClaimTask(ctx, admitted.Accepted.TaskID)
		if err != nil || work.PreconditionSet == nil || work.PreconditionSet.TargetCount != 404 || work.Preconditions == nil || len(work.Preconditions) != 0 {
			t.Fatal(work, err)
		}
		targets := []c.TaskPrecondition{}
		for offset := 0; offset < 404; offset += 200 {
			page, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Offset: offset, Limit: 200})
			if err != nil || page.Total != 404 {
				t.Fatal(page, err)
			}
			targets = append(targets, page.Items...)
		}
		if len(targets) != 404 {
			t.Fatal(len(targets))
		}
		for i, p := range targets {
			if p.Ordinal != int64(i) || p.ItemIndex != 0 || p.AccountID != accounts[i+1].AccountID || !p.SnapshotKnown || p.Missing {
				t.Fatal(i, p)
			}
		}
		current, err := s.GetAccount(ctx, live.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		assertCode(t, targets[0].Check(current), c.CodeVersionConflict)
		empty, err := s.ListTaskPreconditions(ctx, admitted.Accepted.TaskID, c.PageQuery{Offset: 500, Limit: 50})
		if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Total != 404 {
			t.Fatal(empty, err)
		}
	})
}

func TestCaptureFaultRollsBackTaskInputIdempotencyAndSnapshots(t *testing.T) {
	for _, point := range []string{"capture-page", "capture-complete"} {
		t.Run(point, func(t *testing.T) {
			s := openTest(t, "")
			seedAccounts(t, s, 205)
			key := createTestKey(t, s)
			s.fault = func(at string) error {
				if at == point {
					return persistence()
				}
				return nil
			}
			a := testAdmission(key, "atomic-capture", c.TaskReloadAuth)
			_, err := s.AdmitTask(context.Background(), a)
			assertCode(t, err, c.CodePersistenceError)
			for _, table := range []string{"tasks", "task_inputs", "idempotency", "task_preconditions", "task_precondition_sets"} {
				var count int
				if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal(table, count, err)
				}
			}
		})
	}
}

func TestQueuedKeysetIsBoundedReadOnlyAndSkipsClaimedRows(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	ids := []string{}
	for i := range 7 {
		ids = append(ids, admitTest(t, s, key, fmt.Sprintf("queued-%d", i), c.TaskImport).TaskID)
	}
	sort.Strings(ids)
	work, err := s.ClaimTask(ctx, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER query_must_not_update BEFORE UPDATE ON tasks BEGIN SELECT RAISE(ABORT,'unexpected write'); END;"); err != nil {
		t.Fatal(err)
	}
	found := []string{}
	var after *c.QueuedTaskRef
	for {
		page, err := s.ListQueuedTasks(ctx, c.QueuedTaskQuery{After: after, Limit: 2})
		if err != nil || len(page.Items) > 2 {
			t.Fatal(page, err)
		}
		for _, r := range page.Items {
			found = append(found, r.TaskID)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	expectedIDs := append(append([]string{}, ids[:2]...), ids[3:]...)
	if !reflect.DeepEqual(found, expectedIDs) {
		t.Fatal(found, expectedIDs)
	}
	r, err := s.GetTask(ctx, work.Record.TaskID)
	if err != nil || r.Status != c.TaskRunning || r.Revision != work.Record.Revision {
		t.Fatal(r, err)
	}
	_, err = s.ListQueuedTasks(ctx, c.QueuedTaskQuery{Limit: 201})
	assertCode(t, err, c.CodeInvalidRequest)
	end := &c.QueuedTaskRef{TaskID: ids[len(ids)-1], CreatedAt: s.now()}
	empty, err := s.ListQueuedTasks(ctx, c.QueuedTaskQuery{After: end, Limit: 1})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Next != nil {
		t.Fatal(empty, err)
	}
}

func TestClaimPermanentBadStateInterruptsWithoutBlockingQueue(t *testing.T) {
	for _, damage := range []string{"input_missing", "input_invalid", "manifest_missing", "partial_rows"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t, "")
			key := createTestKey(t, s)
			a := testAdmission(key, "bad", c.TaskBatch)
			a.Items = append(a.Items, c.TaskItem{AccountID: id(t, "")})
			bad, err := s.AdmitTask(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			good := admitTest(t, s, key, "good", c.TaskImport)
			statements := map[string]string{"input_missing": "DELETE FROM task_inputs WHERE task_id=?", "input_invalid": "UPDATE task_inputs SET input='{' WHERE task_id=?", "manifest_missing": "DELETE FROM task_precondition_sets WHERE task_id=?", "partial_rows": "DELETE FROM task_preconditions WHERE task_id=? AND ordinal=1"}
			if _, err = s.db.Exec(statements[damage], bad.Accepted.TaskID); err != nil {
				t.Fatal(err)
			}
			_, err = s.ClaimTask(ctx, bad.Accepted.TaskID)
			assertCode(t, err, c.CodeInterrupted)
			r, err := s.GetTask(ctx, bad.Accepted.TaskID)
			if err != nil || r.Status != c.TaskInterrupted {
				t.Fatal(r, err)
			}
			var inputs int
			if s.db.QueryRow("SELECT COUNT(*) FROM task_inputs WHERE task_id=?", r.TaskID).Scan(&inputs) != nil || inputs != 0 {
				t.Fatal("bad private input retained")
			}
			queue, err := s.ListQueuedTasks(ctx, c.QueuedTaskQuery{Limit: 200})
			if err != nil || len(queue.Items) != 1 || queue.Items[0].TaskID != good.TaskID {
				t.Fatal(queue, err)
			}
			if _, err = s.ClaimTask(ctx, good.TaskID); err != nil {
				t.Fatal("healthy work blocked", err)
			}
		})
	}
}

func TestCaptureValidationAfterReplayAndPrivateProjection(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	a := testAdmission(key, "no-capture", c.TaskImport)
	a.Capture = c.TaskCaptureInstruction{}
	_, err := s.AdmitTask(ctx, a)
	assertCode(t, err, c.CodeInvalidRequest)
	a = testAdmission(key, "projection", c.TaskTest)
	admitted, err := s.AdmitTask(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.ClaimTask(ctx, admitted.Accepted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(work)
	if err != nil || string(public) != "{}" {
		t.Fatal("private work serialized", string(public), err)
	}
	var value sql.NullInt64
	if err = s.db.QueryRow("SELECT credential_version FROM task_preconditions WHERE task_id=?", admitted.Accepted.TaskID).Scan(&value); err != nil || value.Valid {
		t.Fatal("missing snapshot has a fabricated version", value, err)
	}
}

func TestRecoveryCannotDowngradeDamagedV2SnapshotToLegacyCAS(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	accounts := seedAccounts(t, s, 1)
	key := createTestKey(t, s)
	credential, state := uint64(2), uint64(1)
	a := testAdmission(key, "lost-v2-manifest", c.TaskReplace)
	a.Items[0].AccountID = accounts[0].AccountID
	a.Capture.Expected = c.ExpectedVersions{Credential: &credential, State: &state}
	a.PrivateInput = json.RawMessage(`{"expected":{"expectedCredentialVersion":2,"expectedStateVersion":1}}`)
	admitted, err := s.AdmitTask(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM task_preconditions WHERE task_id=?", admitted.Accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM task_precondition_sets WHERE task_id=?", admitted.Accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetAccount(ctx, accounts[0].AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: current.AccountID, Expected: expected(current), CredentialDigest: sha256.Sum256([]byte("now equals the formerly future expected pair"))}, newParticipant(t)); err != nil {
		t.Fatal(err)
	}
	safe, err := s.RecoverTasks(ctx)
	if err != nil || len(safe) != 0 {
		t.Fatal(safe, err)
	}
	r, err := s.GetTask(ctx, admitted.Accepted.TaskID)
	if err != nil || r.Status != c.TaskInterrupted {
		t.Fatal("damaged v2 protection was downgraded", r, err)
	}
	if tableCount(t, s, "task_inputs") != 0 || tableCount(t, s, "task_preconditions") != 0 {
		t.Fatal("unsafe legacy repair or private input survived")
	}
}
