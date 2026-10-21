package managementstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Internal envelopes explicitly persist the fields excluded from public JSON.
type diskAccount struct {
	Account  c.Account
	NativeID string
	Deleted  bool
	Digest   [32]byte
}

func packAccount(a c.AccountRecord) diskAccount {
	return diskAccount{a.Account, a.NativeID, a.Deleted, a.CredentialDigest}
}
func (a diskAccount) unpack() c.AccountRecord {
	return c.AccountRecord{Account: a.Account, NativeID: a.NativeID, Deleted: a.Deleted, CredentialDigest: a.Digest}
}

type diskRecovery struct {
	Intent  c.CommitIntent
	Before  *diskAccount
	After   diskAccount
	Receipt *c.UploadReceipt
	Phase   c.CommitPhase
	Ref     string
}

func packRecovery(r c.RecoveryRecord) diskRecovery {
	d := diskRecovery{Intent: r.Plan.Intent, After: packAccount(r.Plan.After), Receipt: r.Plan.Receipt, Phase: r.Phase, Ref: r.ParticipantRef}
	if r.Plan.Before != nil {
		a := packAccount(*r.Plan.Before)
		d.Before = &a
	}
	return d
}
func (d diskRecovery) unpack() c.RecoveryRecord {
	r := c.RecoveryRecord{Plan: c.CommitPlan{Intent: d.Intent, After: d.After.unpack(), Receipt: d.Receipt}, Phase: d.Phase, ParticipantRef: d.Ref}
	if d.Before != nil {
		a := d.Before.unpack()
		r.Plan.Before = &a
	}
	return r
}
func readAccount(ctx context.Context, q queryer, where string, args ...any) (c.AccountRecord, error) {
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT a.record FROM accounts a JOIN identities i ON a.account_id=i.account_id WHERE "+where, args...).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return c.AccountRecord{}, c.NewError(c.CodeNotFound)
	}
	if err != nil {
		return c.AccountRecord{}, persistence()
	}
	var d diskAccount
	if json.Unmarshal(b, &d) != nil {
		return c.AccountRecord{}, persistence()
	}
	return d.unpack(), nil
}
func (s *store) GetAccount(ctx context.Context, id string) (c.AccountRecord, error) {
	a, err := readAccount(ctx, s.db, "a.account_id=?", id)
	if err == nil && a.Deleted {
		return c.AccountRecord{}, c.NewError(c.CodeNotFound)
	}
	return a, err
}
func (s *store) FindAccountByNativeID(ctx context.Context, id string) (c.AccountRecord, error) {
	a, err := readAccount(ctx, s.db, "i.native_id=?", normalizedNative(id))
	if err == nil && a.Deleted {
		return c.AccountRecord{}, c.NewError(c.CodeNotFound)
	}
	return a, err
}
func (s *store) ListAccounts(ctx context.Context, q c.PageQuery) (c.Page[c.Account], error) {
	return page(ctx, s.db, q, "SELECT COUNT(*) FROM accounts WHERE deleted=0", "SELECT record FROM accounts WHERE deleted=0 ORDER BY idx ASC LIMIT ? OFFSET ?", func(rows *sql.Rows) (c.Account, error) {
		d, err := decodeJSON[diskAccount](rows)
		return d.Account, err
	})
}
func writeAccount(ctx context.Context, tx *sql.Tx, a c.AccountRecord) error {
	b, err := json.Marshal(packAccount(a))
	if err != nil {
		return persistence()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO accounts(account_id,idx,deleted,record) VALUES(?,?,?,?)
 ON CONFLICT(account_id) DO UPDATE SET deleted=excluded.deleted,record=excluded.record`, a.AccountID, a.Index, a.Deleted, b)
	if err != nil {
		return persistence()
	}
	return nil
}
func (s *store) allocate(ctx context.Context, tx *sql.Tx, native string) (string, int64, error) {
	var id string
	var index int64
	err := tx.QueryRowContext(ctx, "SELECT account_id,idx FROM identities WHERE native_id=?", native).Scan(&id, &index)
	if err == nil {
		return id, index, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, persistence()
	}
	id, err = s.newID("")
	if err != nil || !c.ValidAccountID(id) {
		return "", 0, persistence()
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO identities(account_id,native_id) VALUES(?,?)", id, native)
	if err != nil {
		return "", 0, persistence()
	}
	index, err = result.LastInsertId()
	if err != nil || index > c.MaxSafeInteger {
		return "", 0, persistence()
	}
	return id, index, nil
}
func (s *store) ReconcileAccounts(ctx context.Context, seeds []c.NativeAccount) error {
	if err := s.lockAccounts(ctx); err != nil {
		return err
	}
	defer s.unlockAccounts()
	return transaction(ctx, s.db, func(tx *sql.Tx) error {
		var pending int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_commits WHERE completed=0").Scan(&pending) != nil {
			return persistence()
		}
		if pending != 0 {
			return c.NewError(c.CodeInvalidState)
		}
		seen := map[string]bool{}
		for _, seed := range seeds {
			native := normalizedNative(seed.NativeID)
			if native == "" || seen[native] {
				return c.NewError(c.CodeInvalidRequest)
			}
			seen[native] = true
			a, err := readAccount(ctx, tx, "i.native_id=?", native)
			if isCode(err, c.CodeNotFound) {
				id, index, err := s.allocate(ctx, tx, native)
				if err != nil {
					return err
				}
				a = c.AccountRecord{Account: c.Account{AccountID: id, Index: index, AccountName: seed.AccountName, AccountState: seed.State, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}}, NativeID: native, CredentialDigest: seed.CredentialDigest}
			} else if err != nil {
				return err
			} else {
				// Missing files never delete or restore a tombstone; archived/deleted state
				// remains management-owned even when an offline file is present.
				if a.Deleted {
					continue
				}
				if a.CredentialDigest != seed.CredentialDigest {
					a.CredentialVersion++
					a.CredentialDigest = seed.CredentialDigest
				}
				if !a.Archived && !reflect.DeepEqual(a.AccountState, seed.State) {
					a.AccountState = seed.State
					a.StateVersion++
				}
				a.AccountName = seed.AccountName
			}
			if a.CredentialVersion > c.MaxSafeInteger || a.StateVersion > c.MaxSafeInteger {
				return c.NewError(c.CodeInvalidState)
			}
			if err := writeAccount(ctx, tx, a); err != nil {
				return err
			}
		}
		return nil
	})
}
func isCode(err error, code c.ErrorCode) bool {
	var e *c.Error
	return errors.As(err, &e) && e.Code == code
}

func (s *store) plan(ctx context.Context, intent c.CommitIntent) (c.RecoveryRecord, error) {
	var r c.RecoveryRecord
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		intent.NativeID = normalizedNative(intent.NativeID)
		if err := intent.Expected.Validate(intent.Kind == c.MutationReplace); err != nil {
			return err
		}
		var before *c.AccountRecord
		var after c.AccountRecord
		if intent.Kind == c.MutationImport {
			if intent.NativeID == "" {
				return c.NewError(c.CodeInvalidRequest)
			}
			existing, err := readAccount(ctx, tx, "i.native_id=?", intent.NativeID)
			if err == nil || existing.AccountID != "" {
				return c.NewError(c.CodeDuplicateAccount)
			}
			if !isCode(err, c.CodeNotFound) {
				return err
			}
			id, index, err := s.allocate(ctx, tx, intent.NativeID)
			if err != nil {
				return err
			}
			after = c.AccountRecord{Account: c.Account{AccountID: id, Index: index, AccountName: intent.AccountName, AccountState: c.AccountState{Enabled: true}, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}}, NativeID: intent.NativeID, CredentialDigest: intent.CredentialDigest}
		} else {
			var a c.AccountRecord
			var err error
			if intent.AccountID != "" {
				a, err = readAccount(ctx, tx, "a.account_id=?", intent.AccountID)
			} else {
				a, err = readAccount(ctx, tx, "i.native_id=?", intent.NativeID)
			}
			if err != nil {
				return err
			}
			if a.Deleted {
				return c.NewError(c.CodeNotFound)
			}
			if intent.NativeID != "" && intent.NativeID != a.NativeID {
				return c.NewError(c.CodeInvalidRequest)
			}
			if intent.Expected.Credential != nil && (*intent.Expected.Credential != a.CredentialVersion || *intent.Expected.State != a.StateVersion) {
				return c.NewError(c.CodeVersionConflict)
			}
			if a.Archived && (intent.Kind == c.MutationReplace || intent.Kind == c.MutationRefresh) {
				return c.NewError(c.CodeInvalidState)
			}
			before = &a
			after = a
			if intent.AccountName != nil {
				after.AccountName = intent.AccountName
			}
			switch intent.Kind {
			case c.MutationReplace:
				after.CredentialVersion++
				after.CredentialDigest = intent.CredentialDigest
			case c.MutationRefresh:
				if after.CredentialDigest != intent.CredentialDigest {
					after.CredentialVersion++
					after.CredentialDigest = intent.CredentialDigest
				}
			case c.MutationState, c.MutationArchive, c.MutationRestore:
			case c.MutationDelete:
				after.Deleted = true
			default:
				return c.NewError(c.CodeInvalidRequest)
			}
			if intent.State != nil {
				after.AccountState = *intent.State
			}
			if intent.Kind == c.MutationArchive {
				after.Archived = true
			}
			if intent.Kind == c.MutationRestore {
				after.Archived = false
			}
			if intent.ProxyChanged || !reflect.DeepEqual(a.AccountState, after.AccountState) {
				after.StateVersion++
			}
		}
		if after.CredentialVersion > c.MaxSafeInteger || after.StateVersion > c.MaxSafeInteger {
			return c.NewError(c.CodeInvalidState)
		}
		var pending int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_commits WHERE account_id=? AND completed=0", after.AccountID).Scan(&pending) != nil {
			return persistence()
		}
		if pending != 0 {
			return c.NewError(c.CodeInvalidState)
		}
		if intent.Upload != nil {
			if intent.Upload.TaskID == "" || intent.Upload.ItemIndex < 0 || (intent.Upload.Kind != c.TaskImport && intent.Upload.Kind != c.TaskReplace) {
				return c.NewError(c.CodeInvalidRequest)
			}
			receipt, err := readReceipt(ctx, tx, *intent.Upload)
			if err != nil {
				return err
			}
			if receipt != nil {
				return c.NewError(c.CodeInvalidState)
			}
		}
		intent.AccountID = after.AccountID
		intent.NativeID = after.NativeID
		r = c.RecoveryRecord{Plan: c.CommitPlan{Intent: intent, Before: before, After: after}, Phase: c.CommitPlanned}
		return insertJournal(ctx, tx, r)
	})
	return r, err
}
func insertJournal(ctx context.Context, tx *sql.Tx, r c.RecoveryRecord) error {
	b, err := json.Marshal(packRecovery(r))
	if err != nil {
		return persistence()
	}
	var task any
	var item any
	if r.Plan.Intent.Upload != nil {
		task = r.Plan.Intent.Upload.TaskID
		item = r.Plan.Intent.Upload.ItemIndex
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO account_commits(operation_id,account_id,phase,record,upload_task_id,upload_item_index) VALUES(?,?,?,?,?,?)", r.Plan.Intent.OperationID, r.Plan.After.AccountID, r.Phase, b, task, item)
	if err != nil {
		return persistence()
	}
	return nil
}
func updateJournal(ctx context.Context, tx *sql.Tx, r c.RecoveryRecord, complete bool) error {
	b, err := json.Marshal(packRecovery(r))
	if err != nil {
		return persistence()
	}
	result, err := tx.ExecContext(ctx, "UPDATE account_commits SET phase=?,record=?,completed=? WHERE operation_id=?", r.Phase, b, complete, r.Plan.Intent.OperationID)
	if err != nil {
		return persistence()
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return persistence()
	}
	return nil
}
func (s *store) saveJournal(ctx context.Context, r c.RecoveryRecord, complete bool) error {
	return transaction(ctx, s.db, func(tx *sql.Tx) error { return updateJournal(ctx, tx, r, complete) })
}
func readJournal(ctx context.Context, q queryer, op string) (c.RecoveryRecord, bool, error) {
	var b []byte
	var done bool
	err := q.QueryRowContext(ctx, "SELECT record,completed FROM account_commits WHERE operation_id=?", op).Scan(&b, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return c.RecoveryRecord{}, false, c.NewError(c.CodeNotFound)
	}
	if err != nil {
		return c.RecoveryRecord{}, false, persistence()
	}
	var d diskRecovery
	if json.Unmarshal(b, &d) != nil {
		return c.RecoveryRecord{}, false, persistence()
	}
	return d.unpack(), done, nil
}

func (s *store) CommitAccount(ctx context.Context, intent c.CommitIntent, p c.AccountCommitParticipant) (c.AccountCommitResult, error) {
	var empty c.AccountCommitResult
	if p == nil || !c.ValidID(intent.OperationID) {
		return empty, c.NewError(c.CodeInvalidRequest)
	}
	if err := s.lockAccounts(ctx); err != nil {
		return empty, err
	}
	defer s.unlockAccounts()
	// A repeated operation can only read its original durable evidence. It never
	// invokes a participant again or derives a receipt from the current account.
	old, _, err := readJournal(ctx, s.db, intent.OperationID)
	if err == nil {
		if old.Phase != c.CommitCommitted {
			return empty, c.NewError(c.CodeInvalidState)
		}
		if !sameOperation(old.Plan.Intent, intent) {
			return empty, c.NewError(c.CodeInvalidState)
		}
		return s.committedResult(ctx, old)
	}
	if !isCode(err, c.CodeNotFound) {
		return empty, err
	}
	r, err := s.plan(ctx, intent)
	if err != nil {
		return empty, err
	}
	abort := func(cause error) (c.AccountCommitResult, error) {
		clean, cancel := cleanupContext()
		defer cancel()
		if p.Rollback(clean, r) != nil {
			return empty, persistence()
		}
		if s.saveJournal(clean, r, true) != nil {
			return empty, persistence()
		}
		return empty, cause
	}
	if err = s.checkpoint("planned"); err != nil {
		return abort(persistence())
	}
	ref, err := p.Prepare(ctx, r.Plan)
	r.ParticipantRef = ref
	if err != nil {
		return abort(persistence())
	}
	if err = s.checkpoint("prepare-returned"); err != nil {
		return abort(persistence())
	}
	r.Phase = c.CommitPrepared
	if err = s.saveJournal(ctx, r, false); err != nil {
		return abort(err)
	}
	if err = s.checkpoint("prepared"); err != nil {
		return abort(persistence())
	}
	if err = p.Apply(ctx, r); err != nil {
		return abort(persistence())
	}
	if err = s.checkpoint("apply-returned"); err != nil {
		return abort(persistence())
	}
	r.Phase = c.CommitApplied
	if err = s.saveJournal(ctx, r, false); err != nil {
		return abort(err)
	}
	if err = s.checkpoint("applied"); err != nil {
		return abort(persistence())
	}
	// Capture the timestamp at the actual commit boundary, never at allocation.
	if r.Plan.Intent.Upload != nil {
		r.Plan.Receipt = &c.UploadReceipt{Status: "committed", AccountID: r.Plan.After.AccountID, Index: r.Plan.After.Index, Versions: r.Plan.After.Versions, CommittedAt: s.now(), ProxyApplied: r.Plan.Intent.ProxyApplied}
	}
	r.Phase = c.CommitCommitted
	err = transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := writeAccount(ctx, tx, r.Plan.After); err != nil {
			return err
		}
		if err := s.checkpoint("metadata-written"); err != nil {
			return err
		}
		if r.Plan.Receipt != nil {
			u := r.Plan.Intent.Upload
			b, err := json.Marshal(r.Plan.Receipt)
			if err != nil {
				return persistence()
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO upload_receipts(task_id,item_index,kind,operation_id,receipt) VALUES(?,?,?,?,?)", u.TaskID, u.ItemIndex, u.Kind, r.Plan.Intent.OperationID, b); err != nil {
				return persistence()
			}
		}
		if err := s.checkpoint("receipt-written"); err != nil {
			return err
		}
		if err := updateJournal(ctx, tx, r, false); err != nil {
			return err
		}
		return s.checkpoint("before-db-commit")
	})
	if err != nil {
		// Commit may have reached disk even when its caller saw a cancellation or
		// driver error. Resolve from the journal before considering file rollback.
		clean, cancel := cleanupContext()
		defer cancel()
		durable, _, readErr := readJournal(clean, s.db, r.Plan.Intent.OperationID)
		if readErr != nil {
			return empty, persistence()
		}
		if durable.Phase != c.CommitCommitted {
			r = durable
			return abort(persistence())
		}
		r = durable
	}
	clean, cancel := cleanupContext()
	defer cancel()
	result, resultErr := s.committedResult(clean, r)
	if resultErr != nil {
		return empty, resultErr
	}
	if s.checkpoint("after-db-commit") != nil {
		return result, persistence()
	}
	if err = p.Finalize(clean, r); err != nil {
		return result, persistence()
	}
	if s.checkpoint("finalize-returned") != nil {
		return result, persistence()
	}
	if err = s.saveJournal(clean, r, true); err != nil {
		return result, err
	}
	return result, nil
}
func sameOperation(a, b c.CommitIntent) bool {
	return a.Kind == b.Kind && (b.AccountID == "" || a.AccountID == b.AccountID) && (b.NativeID == "" || a.NativeID == normalizedNative(b.NativeID)) && reflect.DeepEqual(a.Upload, b.Upload) && a.CredentialDigest == b.CredentialDigest && reflect.DeepEqual(a.State, b.State) && reflect.DeepEqual(a.AccountName, b.AccountName) && reflect.DeepEqual(a.Expected, b.Expected)
}
func (s *store) committedResult(ctx context.Context, r c.RecoveryRecord) (c.AccountCommitResult, error) {
	result := c.AccountCommitResult{Account: r.Plan.After, Changed: r.Plan.Before == nil || !reflect.DeepEqual(*r.Plan.Before, r.Plan.After)}
	if u := r.Plan.Intent.Upload; u != nil {
		receipt, err := readReceipt(ctx, s.db, *u)
		if err != nil {
			return result, err
		}
		if receipt == nil {
			return result, persistence()
		}
		result.Upload = receipt
	}
	return result, nil
}
func (s *store) RecoverAccounts(ctx context.Context, p c.AccountCommitParticipant) error {
	if p == nil {
		return c.NewError(c.CodeInvalidRequest)
	}
	if err := s.lockAccounts(ctx); err != nil {
		return err
	}
	defer s.unlockAccounts()
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM account_commits WHERE completed=0 ORDER BY seq")
	if err != nil {
		return persistence()
	}
	var records []c.RecoveryRecord
	for rows.Next() {
		var b []byte
		var d diskRecovery
		if rows.Scan(&b) != nil || json.Unmarshal(b, &d) != nil {
			rows.Close()
			return persistence()
		}
		records = append(records, d.unpack())
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return persistence()
	}
	for _, r := range records {
		if r.Phase == c.CommitCommitted {
			if _, err = s.committedResult(ctx, r); err != nil {
				return err
			}
			if err = p.Finalize(ctx, r); err != nil {
				return persistence()
			}
		} else {
			if err = p.Rollback(ctx, r); err != nil {
				return persistence()
			}
		}
		if err = s.saveJournal(ctx, r, true); err != nil {
			return err
		}
	}
	return nil
}
func readReceipt(ctx context.Context, q queryer, u c.UploadIdentity) (*c.UploadReceipt, error) {
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT receipt FROM upload_receipts WHERE task_id=? AND item_index=? AND kind=?", u.TaskID, u.ItemIndex, u.Kind).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, persistence()
	}
	var r c.UploadReceipt
	if json.Unmarshal(b, &r) != nil {
		return nil, persistence()
	}
	return &r, nil
}
func (s *store) GetUploadReceipt(ctx context.Context, u c.UploadIdentity) (*c.UploadReceipt, error) {
	return readReceipt(ctx, s.db, u)
}
