package managementstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestKeyDigestAuthenticationExpiryAndPersistentRevocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "keys.db")
	s := openTest(t, path)
	key := createTestKey(t, s)
	if !strings.HasPrefix(key.Token, "mgmt_") || len(key.Token) != 48 {
		t.Fatal("invalid token shape")
	}
	principal, err := s.AuthenticateKey(ctx, key.Token)
	if err != nil || principal.KeyID != key.Key.ID || !principal.Has(c.ScopeAccountsWrite) {
		t.Fatal(principal, err)
	}
	_, err = s.AuthenticateKey(ctx, key.Token+"x")
	assertCode(t, err, c.CodeUnauthorized)
	var digest, metadata []byte
	if err = s.db.QueryRow("SELECT digest,metadata FROM management_keys WHERE key_id=?", key.Key.ID).Scan(&digest, &metadata); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte(key.Token))
	if !reflect.DeepEqual(digest, expectedDigest[:]) || strings.Contains(string(metadata), key.Token) {
		t.Fatal("token persisted")
	}
	if _, err = s.RevokeKey(ctx, key.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevokeKey(ctx, key.Key.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	_, err = s.AuthenticateKey(ctx, key.Token)
	assertCode(t, err, c.CodeUnauthorized)
	keys, err := s.ListKeys(ctx)
	if err != nil || len(keys.Items) != 1 || keys.Items[0].RevokedAt == nil {
		t.Fatal(keys, err)
	}
	expires := c.NewTimestamp(testTime.Add(time.Minute))
	expiring, err := s.CreateKey(ctx, c.KeyCreateRequest{Name: "expires", Scopes: []c.Scope{c.ScopeSystemRead}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return expires.Time }
	_, err = s.AuthenticateKey(ctx, expiring.Token)
	assertCode(t, err, c.CodeUnauthorized)
	active, err := s.IsKeyActive(ctx, expiring.Key.ID)
	if err != nil || active {
		t.Fatal(active, err)
	}
}

func TestOriginalQueuedAckAndIdempotencyConflict(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	a := testAdmission(key, "same", c.TaskImport)
	first, err := s.AdmitTask(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestTaskCancellation(ctx, first.Accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	replay, err := s.AdmitTask(ctx, a)
	if err != nil || !replay.Replayed || replay.Accepted != first.Accepted || replay.Accepted.Status != c.TaskQueued {
		t.Fatal(replay, err)
	}
	a.CanonicalDigest = sha256.Sum256([]byte("different normalized content"))
	_, err = s.AdmitTask(ctx, a)
	assertCode(t, err, c.CodeIdempotencyConflict)
	// Keys belonging to separate principals have independent namespaces.
	another := createTestKey(t, s)
	a.Principal.KeyID = another.Key.ID
	admitted, err := s.AdmitTask(ctx, a)
	if err != nil || admitted.Accepted.TaskID == first.Accepted.TaskID {
		t.Fatal(admitted, err)
	}
	// The old client's empty-items preflight is independent of a missing key.
	a.Items = nil
	a.IdempotencyKey = ""
	_, err = s.AdmitTask(ctx, a)
	assertCode(t, err, c.CodeInvalidRequest)
}

func TestImportClientRefUnicodeBoundariesAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		refs  []string
		valid bool
	}{
		{"non_ascii_short", []string{"企业子号"}, true},
		{"non_ascii_128", []string{strings.Repeat("账", 128)}, true},
		{"non_ascii_129", []string{strings.Repeat("账", 129)}, false},
		{"leading_space", []string{" 账号"}, false},
		{"trailing_unicode_space", []string{"账号\u3000"}, false},
		{"only_unicode_space", []string{"\u3000"}, false},
		{"empty", []string{""}, false},
		{"duplicate_unicode", []string{"账号", "账号"}, false},
		{"distinct_unicode", []string{"账号甲", "账号乙"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t, "")
			key := createTestKey(t, s)
			a := testAdmission(key, tc.name, c.TaskImport)
			a.Items = make([]c.TaskItem, len(tc.refs))
			for i, ref := range tc.refs {
				a.Items[i].ClientRef = ref
			}
			result, err := s.AdmitTask(ctx, a)
			if !tc.valid {
				assertCode(t, err, c.CodeInvalidRequest)
				var tasks, inputs int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&tasks); err != nil {
					t.Fatal(err)
				}
				if err := s.db.QueryRow("SELECT COUNT(*) FROM task_inputs").Scan(&inputs); err != nil {
					t.Fatal(err)
				}
				if tasks != 0 || inputs != 0 {
					t.Fatal("invalid clientRef admitted work", tasks, inputs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.GetTask(ctx, result.Accepted.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Items) != len(tc.refs) {
				t.Fatal("item count changed")
			}
			for i, ref := range tc.refs {
				if r.Items[i].ClientRef != ref {
					t.Fatal("clientRef identity changed", r.Items[i].ClientRef)
				}
			}
		})
	}
}

func TestRevokeAtomicWithConcurrentAdmission(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	start := make(chan struct{})
	results := make(chan error, 24)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			a := testAdmission(key, string(rune('a'+i)), c.TaskImport)
			_, err := s.AdmitTask(ctx, a)
			results <- err
		}(i)
	}
	close(start)
	if _, err := s.RevokeKey(ctx, key.Key.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !isCode(err, c.CodeUnauthorized) {
			t.Fatal(err)
		}
	}
	tasks, err := s.ListTasks(ctx, c.PageQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks.Items {
		if task.Status != c.TaskCancelled {
			t.Fatal("queued work survived revocation", task.Status)
		}
	}
	var private int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM task_inputs").Scan(&private); err != nil || private != 0 {
		t.Fatal(private, err)
	}
	_, err = s.AdmitTask(ctx, testAdmission(key, "late", c.TaskImport))
	assertCode(t, err, c.CodeUnauthorized)
}

func TestBoundedQueueAndStableSQLPagination(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	for _, k := range []string{"one", "two", "three"} {
		admitTest(t, s, key, k, c.TaskImport)
	}
	a := testAdmission(key, "four", c.TaskImport)
	a.QueueLimit = 3
	_, err := s.AdmitTask(ctx, a)
	assertCode(t, err, c.CodeRateLimited)
	first, err := s.ListTasks(ctx, c.PageQuery{Limit: 2})
	if err != nil || first.Total != 3 || len(first.Items) != 2 {
		t.Fatal(first, err)
	}
	next, err := s.ListTasks(ctx, c.PageQuery{Limit: 2, Offset: 2})
	if err != nil || next.Total != 3 || len(next.Items) != 1 {
		t.Fatal(next, err)
	}
	ids := []string{first.Items[0].TaskID, first.Items[1].TaskID, next.Items[0].TaskID}
	if !(ids[0] > ids[1] && ids[1] > ids[2]) {
		t.Fatal("unstable tie ordering", ids)
	}
	empty, err := s.ListTasks(ctx, c.PageQuery{Limit: 1, Offset: 10})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	_, err = s.ListTasks(ctx, c.PageQuery{Limit: 201})
	assertCode(t, err, c.CodeInvalidRequest)
}

func TestTaskRevisionProjectionAndPrivateInputErasure(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	r := admitTest(t, s, key, "private", c.TaskImport)
	public, err := json.Marshal(r)
	if err != nil || strings.Contains(string(public), "private-cookie-marker") {
		t.Fatal(string(public), err)
	}
	work, err := s.ClaimTask(ctx, r.TaskID)
	if err != nil || !strings.Contains(string(work.PrivateInput), "private-cookie-marker") {
		t.Fatal(err)
	}
	_, err = s.SaveTask(ctx, r, r.Revision)
	assertCode(t, err, c.CodeVersionConflict)
	r = work.Record
	r.Items[0].Status = c.TaskRunning
	r.Items[0].Stage = "preparing"
	r.Items[0].Progress = 10
	saved, err := s.SaveTask(ctx, r, r.Revision)
	if err != nil {
		t.Fatal(err)
	}
	regressed := saved
	regressed.Items = append([]c.TaskItem{}, saved.Items...)
	regressed.Items[0].Progress = 1
	_, err = s.SaveTask(ctx, regressed, saved.Revision)
	assertCode(t, err, c.CodeInvalidState)
	forged := saved
	forged.Items = append([]c.TaskItem{}, saved.Items...)
	forged.Items[0].Upload = &c.UploadReceipt{Status: "committed", AccountID: id(t, ""), Index: 1, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}, CommittedAt: s.now()}
	_, err = s.SaveTask(ctx, forged, saved.Revision)
	assertCode(t, err, c.CodeInvalidState)
	done := saved
	done.Items = append([]c.TaskItem{}, saved.Items...)
	done.Status = c.TaskFailed
	done.Items[0].Status = c.TaskFailed
	done.Items[0].Stage = "failed"
	done.Items[0].Progress = 100
	done.Items[0].Error = &c.Error{Code: c.CodeInvalidCredentials, Message: "private-cookie-marker"}
	terminal, err := s.SaveTask(ctx, done, saved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Items[0].Error.Message == "private-cookie-marker" {
		t.Fatal("freeform error leaked")
	}
	var private int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM task_inputs WHERE task_id=?", r.TaskID).Scan(&private); err != nil || private != 0 {
		t.Fatal(private, err)
	}
	_, err = s.ClaimTask(ctx, r.TaskID)
	assertCode(t, err, c.CodeInvalidState)
	_, err = s.SaveTask(ctx, terminal, terminal.Revision)
	assertCode(t, err, c.CodeInvalidState)
}

func TestRecoveryOnlyReturnsUnexecutedQueuedTasks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	s := openTest(t, path)
	key := createTestKey(t, s)
	p := newParticipant(t)
	safe := admitTest(t, s, key, "safe", c.TaskImport)
	running := admitTest(t, s, key, "running", c.TaskTest)
	if _, err := s.ClaimTask(ctx, running.TaskID); err != nil {
		t.Fatal(err)
	}
	queuedCommitted := admitTest(t, s, key, "committed", c.TaskImport)
	upload := &c.UploadIdentity{TaskID: queuedCommitted.TaskID, ItemIndex: 0, Kind: c.TaskImport}
	firstCommit := importTest(t, s, p, "committed@example.invalid", upload)
	if _, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: firstCommit.Account.AccountID, Expected: expected(firstCommit.Account), CredentialDigest: sha256.Sum256([]byte("newer synthetic bytes"))}, p); err != nil {
		t.Fatal(err)
	}
	queuedAttempted := admitTest(t, s, key, "attempted", c.TaskImport)
	p.fail = "prepare"
	_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "attempted@example.invalid", Upload: &c.UploadIdentity{TaskID: queuedAttempted.TaskID, ItemIndex: 0, Kind: c.TaskImport}}, p)
	assertCode(t, err, c.CodePersistenceError)
	p.fail = ""
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if err = s.RecoverAccounts(ctx, p); err != nil {
		t.Fatal(err)
	}
	ids, err := s.RecoverTasks(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{safe.TaskID}) {
		t.Fatal(ids, err)
	}
	for _, task := range []c.TaskRecord{running, queuedCommitted, queuedAttempted} {
		r, err := s.GetTask(ctx, task.TaskID)
		if err != nil || r.Status != c.TaskInterrupted {
			t.Fatal(r, err)
		}
		var private int
		if s.db.QueryRow("SELECT COUNT(*) FROM task_inputs WHERE task_id=?", r.TaskID).Scan(&private) != nil || private != 0 {
			t.Fatal("interrupted private input retained")
		}
	}
	committed, err := s.GetTask(ctx, queuedCommitted.TaskID)
	if err != nil || committed.Items[0].Upload == nil || committed.Items[0].Result.Upload == nil {
		t.Fatal(committed, err)
	}
	if committed.Items[0].Upload.CredentialVersion != 1 {
		t.Fatal("receipt was reconstructed from the newer live account")
	}
	attempted, err := s.GetTask(ctx, queuedAttempted.TaskID)
	if err != nil || attempted.Items[0].Upload != nil {
		t.Fatal("invented uncommitted receipt", err)
	}
	// Startup recovery reads metadata and never invokes another participant Apply.
	if p.calls["apply"] != 2 {
		t.Fatal("write replay", p.calls)
	}
}

func TestCommittedCancellationAndPruningKeepHistoricalReceipt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	r := admitTest(t, s, key, "retain", c.TaskImport)
	p := newParticipant(t)
	u := &c.UploadIdentity{TaskID: r.TaskID, ItemIndex: 0, Kind: c.TaskImport}
	result := importTest(t, s, p, "retain@example.invalid", u)
	canceled, err := s.RequestTaskCancellation(ctx, r.TaskID)
	if err != nil || canceled.Items[0].Upload == nil || canceled.Status != c.TaskCancelled {
		t.Fatal(canceled, err)
	}
	if err = s.Prune(ctx, c.NewTimestamp(testTime.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTask(ctx, r.TaskID); err != nil {
		t.Fatal("retention shortened", err)
	}
	s.clock = func() time.Time { return testTime.Add(31 * 24 * time.Hour) }
	if err = s.Prune(ctx, s.now()); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetTask(ctx, r.TaskID)
	assertCode(t, err, c.CodeNotFound)
	receipt, err := s.GetUploadReceipt(ctx, *u)
	if err != nil || !reflect.DeepEqual(receipt, result.Upload) {
		t.Fatal("receipt pruned", receipt, err)
	}
	current, err := s.GetAccount(ctx, result.Account.AccountID)
	if err != nil || current.Index != result.Account.Index {
		t.Fatal(current, err)
	}
	var idem int
	if s.db.QueryRow("SELECT COUNT(*) FROM idempotency").Scan(&idem) != nil || idem != 0 {
		t.Fatal("orphan idempotency")
	}
}

func TestImmutableReceiptSQLGuardAndDuplicateOperation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	p := newParticipant(t)
	u := &c.UploadIdentity{TaskID: id(t, "task"), ItemIndex: 0, Kind: c.TaskImport}
	in := c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "once@example.invalid", Upload: u, CredentialDigest: sha256.Sum256(p.after)}
	original, err := s.CommitAccount(ctx, in, p)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.CommitAccount(ctx, in, p)
	if err != nil || !reflect.DeepEqual(original.Upload, repeated.Upload) || p.calls["apply"] != 1 {
		t.Fatal(repeated, err, p.calls)
	}
	if _, err = s.db.Exec("UPDATE upload_receipts SET receipt='{}'"); err == nil {
		t.Fatal("receipt update allowed")
	}
	if _, err = s.db.Exec("DELETE FROM upload_receipts"); err == nil {
		t.Fatal("receipt delete allowed")
	}
	in.OperationID = id(t, "op")
	in.NativeID = "another@example.invalid"
	_, err = s.CommitAccount(ctx, in, p)
	assertCode(t, err, c.CodeInvalidState)
	in.OperationID = mustID("op")
	in.Upload = nil
	if _, err = s.CommitAccount(ctx, in, p); err != nil {
		t.Fatal(err)
	}
}

func TestAuditAllowlistAndStablePaging(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	for range 3 {
		e := c.AuditEntry{AuditID: id(t, "audit"), RequestID: id(t, "req"), CreatedByKeyID: key.Key.ID, Action: "import", Timestamp: s.now(), Outcome: "failed", Error: &c.Error{Code: c.CodeInvalidCredentials, Message: "private-cookie-marker"}}
		if err := s.AppendAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListAudit(ctx, c.PageQuery{Limit: 2})
	if err != nil || first.Total != 3 || len(first.Items) != 2 || first.Items[0].AuditID <= first.Items[1].AuditID {
		t.Fatal(first, err)
	}
	public, _ := json.Marshal(first)
	if strings.Contains(string(public), "private-cookie-marker") {
		t.Fatal("audit private data leaked")
	}
	e := first.Items[0]
	e.AuditID = id(t, "audit")
	e.Action = "private-cookie-marker"
	assertCode(t, s.AppendAudit(ctx, e), c.CodeInvalidRequest)
}
