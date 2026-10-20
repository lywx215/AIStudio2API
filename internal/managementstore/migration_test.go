package managementstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Captured before S2 changes: this is the actual v1 DDL, not the v2 schema with
// its version number edited. Real SQLite reopen exercises the migration path.
//
//go:embed testdata/schema_v1.sql
var schemaV1 string

func legacyStore(t *testing.T) (*store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schemaV1); err != nil {
		db.Close()
		t.Fatal(err)
	}
	s := &store{db: db, clock: func() time.Time { return testTime }, newID: c.NewID, accountGate: make(chan struct{}, 1)}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func insertLegacyTask(t *testing.T, s *store, key c.KeyCreated, name string, kind c.TaskKind, status c.TaskState, input json.RawMessage, account string) (c.TaskRecord, [32]byte) {
	t.Helper()
	now := s.now()
	item := c.TaskItem{ClientRef: "legacy", AccountID: account, Status: c.TaskQueued, Stage: "queued"}
	if kind == c.TaskImport || kind == c.TaskReloadAuth {
		item.AccountID = ""
	}
	r := c.TaskRecord{Task: c.Task{TaskID: id(t, "task"), Kind: kind, Status: status, CreatedByKeyID: key.Key.ID, CreatedAt: now, UpdatedAt: now, Items: []c.TaskItem{item}}, Revision: 1}
	if status == c.TaskRunning {
		r.StartedAt = &now
		r.Items[0].Status = c.TaskRunning
		r.Items[0].Stage = "running"
	}
	if status.Terminal() {
		r.StartedAt = &now
		r.FinishedAt = &now
		r.Items[0].Status = status
		r.Items[0].Stage = string(status)
		r.Items[0].Progress = 100
	}
	r.Counts = taskCounts(r.Items)
	projection, err := json.Marshal(r.Task)
	if err != nil {
		t.Fatal(err)
	}
	acceptance, _ := json.Marshal(c.TaskAccepted{TaskID: r.TaskID, Status: c.TaskQueued})
	digest := sha256.Sum256([]byte(name))
	var finished any
	if r.FinishedAt != nil {
		finished = r.FinishedAt.UnixMilli()
	}
	if err = transaction(context.Background(), s.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO tasks(task_id,key_id,kind,status,created_at,updated_at,finished_at,revision,projection) VALUES(?,?,?,?,?,?,?,?,?)", r.TaskID, key.Key.ID, kind, status, now.UnixMilli(), now.UnixMilli(), finished, r.Revision, projection); err != nil {
			return err
		}
		if input != nil {
			if _, err := tx.Exec("INSERT INTO task_inputs(task_id,input) VALUES(?,?)", r.TaskID, []byte(input)); err != nil {
				return err
			}
		}
		_, err := tx.Exec("INSERT INTO idempotency(key_id,idempotency_key,digest,task_id,acceptance) VALUES(?,?,?,?,?)", key.Key.ID, name, digest[:], r.TaskID, acceptance)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r, digest
}

func TestSQLiteV1UpgradeLegacyRecoveryAndIdempotentReopen(t *testing.T) {
	ctx := context.Background()
	old, path := legacyStore(t)
	key := createTestKey(t, old)
	target := id(t, "")
	credential, state := uint64(7), uint64(4)
	expectedPair := c.ExpectedVersions{Credential: &credential, State: &state}
	explicitBody, err := json.Marshal(struct {
		Expected c.ExpectedVersions `json:"expected"`
	}{expectedPair})
	if err != nil {
		t.Fatal(err)
	}
	importTask, importDigest := insertLegacyTask(t, old, key, "legacy-import", c.TaskImport, c.TaskQueued, json.RawMessage(`{"syntheticCookie":"legacy-only"}`), "")
	explicitTask, _ := insertLegacyTask(t, old, key, "legacy-explicit", c.TaskReplace, c.TaskQueued, explicitBody, target)
	unsafe := []c.TaskRecord{}
	for _, kind := range []c.TaskKind{c.TaskReplace, c.TaskTest, c.TaskArchive, c.TaskRestore, c.TaskReload, c.TaskBatch, c.TaskReloadAuth} {
		r, _ := insertLegacyTask(t, old, key, "unsafe-"+string(kind), kind, c.TaskQueued, json.RawMessage(`{"legacy":true}`), target)
		unsafe = append(unsafe, r)
	}
	partial, _ := insertLegacyTask(t, old, key, "partial-pair", c.TaskReplace, c.TaskQueued, json.RawMessage(`{"expected":{"expectedCredentialVersion":1}}`), target)
	unsafe = append(unsafe, partial)
	running, _ := insertLegacyTask(t, old, key, "running", c.TaskImport, c.TaskRunning, json.RawMessage(`{"syntheticCookie":"must-clear"}`), "")
	unsafe = append(unsafe, running)
	terminal, _ := insertLegacyTask(t, old, key, "terminal", c.TaskTest, c.TaskFailed, json.RawMessage(`{"syntheticCookie":"terminal-clear"}`), target)
	committed, _ := insertLegacyTask(t, old, key, "committed", c.TaskReplace, c.TaskQueued, explicitBody, target)
	receipt := c.UploadReceipt{Status: "committed", AccountID: target, Index: 22, Versions: c.Versions{CredentialVersion: 6, StateVersion: 3}, CommittedAt: old.now()}
	raw, _ := json.Marshal(receipt)
	if _, err = old.db.Exec("INSERT INTO upload_receipts(task_id,item_index,kind,operation_id,receipt) VALUES(?,?,?,?,?)", committed.TaskID, 0, c.TaskReplace, id(t, "op"), raw); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	var version, sets int
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 2 {
		t.Fatal(version)
	}
	if s.db.QueryRow("SELECT COUNT(*) FROM task_precondition_sets").Scan(&sets) != nil || sets != 0 {
		t.Fatal("DDL migration invented protection")
	}
	beforeRecovery, err := s.GetTask(ctx, unsafe[0].TaskID)
	if err != nil || beforeRecovery.Status != c.TaskQueued {
		t.Fatal("migration performed recovery before account recovery", err)
	}
	// An unchanged terminal task cannot be rewritten during either recovery run.
	if _, err = s.db.Exec("CREATE TRIGGER immutable_terminal_history BEFORE UPDATE ON tasks WHEN OLD.task_id='" + terminal.TaskID + "' BEGIN SELECT RAISE(ABORT,'terminal history rewritten'); END;"); err != nil {
		t.Fatal(err)
	}
	safe, err := s.RecoverTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(safe)
	wanted := []string{importTask.TaskID, explicitTask.TaskID}
	sort.Strings(wanted)
	if !reflect.DeepEqual(safe, wanted) {
		t.Fatal(safe, wanted)
	}
	for _, legacy := range append(unsafe, committed) {
		r, err := s.GetTask(ctx, legacy.TaskID)
		if err != nil || r.Status != c.TaskInterrupted {
			t.Fatal(r, err)
		}
		var count int
		if s.db.QueryRow("SELECT COUNT(*) FROM task_inputs WHERE task_id=?", r.TaskID).Scan(&count) != nil || count != 0 {
			t.Fatal("legacy unsafe input retained")
		}
	}
	protected, err := s.ListTaskPreconditions(ctx, explicitTask.TaskID, c.PageQuery{Limit: 1})
	if err != nil || len(protected.Items) != 1 {
		t.Fatal(protected, err)
	}
	p := protected.Items[0]
	if p.SnapshotKnown || p.Missing || p.Versions != (c.Versions{}) || !reflect.DeepEqual(p.ClientExpected, expectedPair) {
		t.Fatal("legacy pair replaced with a current snapshot", p)
	}
	// There is no live account at all: migration/recovery cannot have fabricated
	// credentialVersion=7/stateVersion=4 from current state.
	_, err = s.GetAccount(ctx, target)
	assertCode(t, err, c.CodeNotFound)
	reread, err := s.GetTask(ctx, terminal.TaskID)
	if err != nil || !reflect.DeepEqual(reread, terminal) {
		t.Fatal("terminal history changed", reread, err)
	}
	originalReceipt, err := s.GetUploadReceipt(ctx, c.UploadIdentity{TaskID: committed.TaskID, ItemIndex: 0, Kind: c.TaskReplace})
	if err != nil || !reflect.DeepEqual(*originalReceipt, receipt) {
		t.Fatal(originalReceipt, err)
	}
	replay, err := s.AdmitTask(ctx, c.Admission{Principal: c.Principal{KeyID: key.Key.ID}, IdempotencyKey: "legacy-import", CanonicalDigest: importDigest, Kind: c.TaskImport, Items: []c.TaskItem{{ClientRef: "legacy"}}, PrivateInput: json.RawMessage(`{}`), QueueLimit: 1})
	if err != nil || !replay.Replayed || replay.Accepted.TaskID != importTask.TaskID || replay.Accepted.Status != c.TaskQueued {
		t.Fatal(replay, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	safeAgain, err := s.RecoverTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(safeAgain)
	if !reflect.DeepEqual(safeAgain, wanted) {
		t.Fatal(safeAgain, err)
	}
	after, err := s.ListTaskPreconditions(ctx, explicitTask.TaskID, c.PageQuery{Limit: 1})
	if err != nil || !reflect.DeepEqual(after.Items, protected.Items) {
		t.Fatal("reopen recaptured legacy versions", after, err)
	}
	work, err := s.ClaimTask(ctx, explicitTask.TaskID)
	if err != nil || work.PreconditionSet == nil || work.Preconditions[0].SnapshotKnown || *work.Preconditions[0].Expected().Credential != 7 {
		t.Fatal(work, err)
	}
}

func TestSQLiteV1MigrationFailureRollsBackDDLAndCanResume(t *testing.T) {
	ctx := context.Background()
	s, path := legacyStore(t)
	key := createTestKey(t, s)
	task, _ := insertLegacyTask(t, s, key, "unmodified", c.TaskImport, c.TaskQueued, json.RawMessage(`{}`), "")
	s.fault = func(point string) error {
		if point == "migration-before-commit" {
			return persistence()
		}
		return nil
	}
	assertCode(t, s.migrate(ctx), c.CodePersistenceError)
	var version, tables int
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 1 {
		t.Fatal("version changed on rollback", version)
	}
	if s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('task_preconditions','task_precondition_sets')").Scan(&tables) != nil || tables != 0 {
		t.Fatal("partial DDL persisted", tables)
	}
	current, err := s.GetTask(ctx, task.TaskID)
	if err != nil || !reflect.DeepEqual(current, task) {
		t.Fatal(current, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if err = s.migrate(ctx); err != nil {
		t.Fatal("idempotent migration", err)
	}
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 2 {
		t.Fatal(version)
	}
	current, err = s.GetTask(ctx, task.TaskID)
	if err != nil || !reflect.DeepEqual(current, task) {
		t.Fatal(current, err)
	}
}

func TestRecoveryDoesNotRewriteUnchangedTerminalProjection(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, "")
	key := createTestKey(t, s)
	r := admitTest(t, s, key, "terminal-stable", c.TaskImport)
	r, err := s.RequestTaskCancellation(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER refuse_task_rewrite BEFORE UPDATE ON tasks BEGIN SELECT RAISE(ABORT,'unexpected task rewrite'); END;"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO task_inputs(task_id,input) VALUES(?,?)", r.TaskID, []byte(`{"syntheticCookie":"erase-only"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverTasks(ctx); err != nil {
		t.Fatal(err)
	}
	reread, err := s.GetTask(ctx, r.TaskID)
	if err != nil || !reflect.DeepEqual(reread, r) {
		t.Fatal(reread, err)
	}
	var private int
	if s.db.QueryRow("SELECT COUNT(*) FROM task_inputs").Scan(&private) != nil || private != 0 {
		t.Fatal("terminal input was not erased")
	}
}

func TestSQLiteFutureSchemaFailsClosed(t *testing.T) {
	s, path := legacyStore(t)
	if _, err := s.db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), c.StoreOptions{Path: path})
	assertCode(t, err, c.CodePersistenceError)
}
