package managementstore

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestAccountCrashBoundariesAndRestart(t *testing.T) {
	// Stops immediately before/after every externally visible durable boundary.
	cases := []struct {
		point       string
		participant bool
		committed   bool
	}{
		{"planned", false, false}, {"prepare", true, false}, {"prepare-returned", false, false},
		{"prepared", false, false}, {"apply", true, false}, {"apply-returned", false, false},
		{"applied", false, false}, {"before-db-commit", false, false},
		{"metadata-written", false, false}, {"receipt-written", false, false},
		{"after-db-commit", false, true}, {"finalize", true, true}, {"finalize-returned", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "crash.db")
			s := openTest(t, path)
			p := newParticipant(t)
			if err := os.WriteFile(p.target(), []byte("synthetic-before"), 0600); err != nil {
				t.Fatal(err)
			}
			u := &c.UploadIdentity{TaskID: id(t, "task"), ItemIndex: 0, Kind: c.TaskImport}
			intent := c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "case@example.invalid", CredentialDigest: sha256.Sum256(p.after), Upload: u}
			if tc.participant {
				p.crash = tc.point
			} else {
				s.fault = func(point string) error {
					if point == tc.point {
						panic("simulated crash")
					}
					return nil
				}
			}
			captureCrash(t, func() { _, _ = s.CommitAccount(ctx, intent, p) })
			// Opening a fresh Store verifies persistent records, rather than in-memory flags.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTest(t, path)
			p.crash = ""
			p.fail = ""
			if _, err := s.RecoverTasks(ctx); !isCode(err, c.CodeInvalidState) {
				t.Fatalf("task recovery accepted unresolved account log: %v", err)
			}
			if err := s.RecoverAccounts(ctx, p); err != nil {
				t.Fatal(err)
			}
			if err := s.RecoverAccounts(ctx, p); err != nil {
				t.Fatal("second recovery", err)
			}
			account, err := s.FindAccountByNativeID(ctx, intent.NativeID)
			receipt, receiptErr := s.GetUploadReceipt(ctx, *u)
			if receiptErr != nil {
				t.Fatal(receiptErr)
			}
			bytes, readErr := os.ReadFile(p.target())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.committed {
				if err != nil || receipt == nil || receipt.AccountID != account.AccountID || receipt.Status != "committed" || !reflect.DeepEqual(receipt.Versions, account.Versions) {
					t.Fatalf("missing atomic commit: %+v %+v %v", account, receipt, err)
				}
				if string(bytes) != string(p.after) {
					t.Fatal("committed files rolled back")
				}
			} else {
				assertCode(t, err, c.CodeNotFound)
				if receipt != nil {
					t.Fatal("uncommitted receipt invented")
				}
				if string(bytes) != "synthetic-before" {
					t.Fatal("before image not restored")
				}
			}
		})
	}
}

func TestCommitTransactionFailureNeverPublishesHalfAReceipt(t *testing.T) {
	for _, point := range []string{"metadata-written", "receipt-written", "before-db-commit"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t, "")
			p := newParticipant(t)
			u := &c.UploadIdentity{TaskID: id(t, "task"), ItemIndex: 0, Kind: c.TaskImport}
			s.fault = func(stage string) error {
				if stage == point {
					return persistence()
				}
				return nil
			}
			_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "atomic@example.invalid", Upload: u}, p)
			assertCode(t, err, c.CodePersistenceError)
			_, err = s.FindAccountByNativeID(ctx, "atomic@example.invalid")
			assertCode(t, err, c.CodeNotFound)
			receipt, err := s.GetUploadReceipt(ctx, *u)
			if err != nil || receipt != nil {
				t.Fatal("partial commit escaped", receipt, err)
			}
			if _, err = os.Stat(p.target()); !os.IsNotExist(err) {
				t.Fatal("uncommitted file not restored", err)
			}
		})
	}
}

func TestCancellationAfterDurableCommitKeepsEvidenceAndFinalizes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := openTest(t, "")
	p := newParticipant(t)
	u := &c.UploadIdentity{TaskID: id(t, "task"), ItemIndex: 0, Kind: c.TaskImport}
	s.fault = func(point string) error {
		if point == "after-db-commit" {
			cancel()
		}
		return nil
	}
	result, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "cancel@example.invalid", Upload: u}, p)
	if err != nil || result.Upload == nil || p.calls["finalize"] != 1 || p.calls["rollback"] != 0 {
		t.Fatal(result, err, p.calls)
	}
	receipt, err := s.GetUploadReceipt(context.Background(), *u)
	if err != nil || !reflect.DeepEqual(receipt, result.Upload) {
		t.Fatal(receipt, err)
	}
}

func TestParticipantErrorsRollbackAndCleanupFailureBlocksNextWriter(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	p := newParticipant(t)
	p.fail = "prepare"
	_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "failed@example.invalid"}, p)
	assertCode(t, err, c.CodePersistenceError)
	if strings.Contains(err.Error(), "private-cookie-marker") {
		t.Fatal("private error leaked")
	}
	p.fail = ""
	a := importTest(t, s, p, "good@example.invalid", nil).Account
	p.fail = "finalize"
	intent := c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: a.AccountID, Expected: expected(a), CredentialDigest: sha256.Sum256([]byte("other"))}
	result, err := s.CommitAccount(ctx, intent, p)
	assertCode(t, err, c.CodePersistenceError)
	if result.Account.CredentialVersion != 2 {
		t.Fatal("durable result lost")
	}
	next := intent
	next.OperationID = id(t, "op")
	next.Expected = expected(result.Account)
	_, err = s.CommitAccount(ctx, next, p)
	assertCode(t, err, c.CodeInvalidState)
	p.fail = ""
	if err = s.RecoverAccounts(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitAccount(ctx, next, p); err != nil {
		t.Fatal(err)
	}
}

func TestParticipantNeverRunsInsideSQLiteTransaction(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	p := newParticipant(t)
	p.probe = func(_ context.Context) error {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		// MaxOpenConns=1 makes an accidental open transaction deterministically fail.
		var count int
		return s.db.QueryRowContext(probe, "SELECT COUNT(*) FROM identities").Scan(&count)
	}
	importTest(t, s, p, "no-transaction@example.invalid", nil)
	p.fail = "prepare"
	_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "rollback-probe@example.invalid"}, p)
	assertCode(t, err, c.CodePersistenceError)
}

func TestConcurrentDualCASAndOriginalReceipt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	p := newParticipant(t)
	u := &c.UploadIdentity{TaskID: id(t, "task"), ItemIndex: 0, Kind: c.TaskImport}
	first := importTest(t, s, p, "same@example.invalid", u)
	original, err := s.GetUploadReceipt(ctx, *u)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: mustID("op"), Kind: c.MutationReplace, AccountID: first.Account.AccountID, Expected: expected(first.Account), CredentialDigest: sha256.Sum256([]byte("repeat"))}, p)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if isCode(err, c.CodeVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := s.GetAccount(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != 2 || current.StateVersion != 1 {
		t.Fatal(current.Versions)
	}
	receipt, err := s.GetUploadReceipt(ctx, *u)
	if err != nil || !reflect.DeepEqual(receipt, original) {
		t.Fatal("receipt mutated", err)
	}
	// Even an unchanged credential replacement must invalidate prior evidence.
	currentResult, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: current.AccountID, Expected: expected(current), CredentialDigest: current.CredentialDigest}, p)
	if err != nil || currentResult.Account.CredentialVersion != 3 {
		t.Fatal(currentResult, err)
	}
	// A stale state CAS cannot overwrite a newer manual disable.
	stale := currentResult.Account
	disabled := stale.AccountState
	disabled.Enabled = false
	reason := "manual"
	disabled.DisabledReason = &reason
	newer, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationState, AccountID: stale.AccountID, Expected: expected(stale), State: &disabled}, p)
	if err != nil {
		t.Fatal(err)
	}
	enabled := stale.AccountState
	_, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: stale.AccountID, Expected: expected(stale), State: &enabled}, p)
	assertCode(t, err, c.CodeVersionConflict)
	if newer.Account.StateVersion != 2 {
		t.Fatal(newer.Account.Versions)
	}
}
func mustID(prefix string) string {
	v, err := c.NewID(prefix)
	if err != nil {
		panic(err)
	}
	return v
}

func TestIndexTombstonesAndReconcile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.db")
	s := openTest(t, path)
	p := newParticipant(t)
	p.fail = "prepare"
	_, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "reserved@example.invalid"}, p)
	assertCode(t, err, c.CodePersistenceError)
	p.fail = ""
	a := importTest(t, s, p, " A@Example.Invalid ", nil).Account
	if a.Index < 2 || a.NativeID != "a@example.invalid" {
		t.Fatal("allocation high water or normalization", a.Index, a.NativeID)
	}
	_, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: "A@example.invalid"}, p)
	assertCode(t, err, c.CodeDuplicateAccount)
	deleted, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationDelete, AccountID: a.AccountID}, p)
	if err != nil || !deleted.Account.Deleted {
		t.Fatal(err)
	}
	b := importTest(t, s, p, "b@example.invalid", nil).Account
	if b.Index <= a.Index || b.AccountID == a.AccountID {
		t.Fatal("reused identity")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	seed := c.NativeAccount{NativeID: b.NativeID, State: b.AccountState, CredentialDigest: sha256.Sum256([]byte("offline-change"))}
	if err = s.ReconcileAccounts(ctx, []c.NativeAccount{seed, {NativeID: a.NativeID, State: c.AccountState{Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	reread, err := s.FindAccountByNativeID(ctx, b.NativeID)
	if err != nil || reread.AccountID != b.AccountID || reread.Index != b.Index || reread.CredentialVersion != 2 {
		t.Fatal(reread, err)
	}
	_, err = s.GetAccount(ctx, a.AccountID)
	assertCode(t, err, c.CodeNotFound)
	empty, err := s.ListAccounts(ctx, c.PageQuery{Limit: 1, Offset: 10})
	if err != nil || len(empty.Items) != 0 || empty.Items == nil || empty.Total != 1 {
		t.Fatal(empty, err)
	}
}

func TestArchiveRestoreAndDigestRefresh(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	p := newParticipant(t)
	a := importTest(t, s, p, "archive@example.invalid", nil).Account
	state := a.AccountState
	state.Enabled = false
	manual := "manual"
	state.DisabledReason = &manual
	aResult, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationState, AccountID: a.AccountID, State: &state}, p)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationArchive, AccountID: a.AccountID}, p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationReplace, AccountID: a.AccountID, Expected: expected(archived.Account)}, p)
	assertCode(t, err, c.CodeInvalidState)
	restored, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationRestore, AccountID: a.AccountID}, p)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Account.AccountID != a.AccountID || restored.Account.Index != a.Index || restored.Account.Enabled || *restored.Account.DisabledReason != "manual" || restored.Account.StateVersion != aResult.Account.StateVersion+2 {
		t.Fatal(restored)
	}
	refreshed, err := s.CommitAccount(ctx, c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationRefresh, AccountID: a.AccountID, Expected: expected(restored.Account), CredentialDigest: restored.Account.CredentialDigest}, p)
	if err != nil || refreshed.Account.CredentialVersion != 1 {
		t.Fatal(refreshed, err)
	}
}
