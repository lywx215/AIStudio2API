package managementstore

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func validKind(kind c.TaskKind) bool {
	switch kind {
	case c.TaskImport, c.TaskReplace, c.TaskTest, c.TaskArchive, c.TaskRestore, c.TaskReload, c.TaskReloadAuth, c.TaskBatch:
		return true
	}
	return false
}
func validStage(stage string) bool {
	switch stage {
	case "queued", "running", "preparing", "draining", "committing", "uploaded", "verifying", "opening", "connecting", "generating", "completed", "failed", "cancelled", "interrupted", "connection_verified", "model_verified":
		return true
	}
	return false
}
func (s *store) AdmitTask(ctx context.Context, a c.Admission) (c.AdmittedTask, error) {
	var result c.AdmittedTask
	// Shape checks precede the idempotency-key check, including an empty import.
	if !validKind(a.Kind) || len(a.Items) == 0 || len(a.Items) > c.MaxBatchItems || !json.Valid(a.PrivateInput) || len(a.PrivateInput) > c.MaxBodyBytes || !c.ValidID(a.Principal.KeyID) {
		return result, c.NewError(c.CodeInvalidRequest)
	}
	if strings.TrimSpace(a.IdempotencyKey) == "" {
		return result, c.NewError(c.CodeIdempotencyKeyRequired)
	}
	if len(a.IdempotencyKey) > 256 {
		return result, c.NewError(c.CodeInvalidRequest)
	}
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		active, err := keyActive(ctx, tx, a.Principal.KeyID, s.now().UnixMilli())
		if err != nil {
			return err
		}
		if !active {
			return c.NewError(c.CodeUnauthorized)
		}
		var digest, b []byte
		err = tx.QueryRowContext(ctx, "SELECT digest,acceptance FROM idempotency WHERE key_id=? AND idempotency_key=?", a.Principal.KeyID, a.IdempotencyKey).Scan(&digest, &b)
		if err == nil {
			if subtle.ConstantTimeCompare(digest, a.CanonicalDigest[:]) != 1 {
				return c.NewError(c.CodeIdempotencyConflict)
			}
			if json.Unmarshal(b, &result.Accepted) != nil {
				return persistence()
			}
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return persistence()
		}
		if err := validateCapture(a.Kind, a.Items, a.Capture); err != nil {
			return err
		}
		limit := a.QueueLimit
		if limit <= 0 {
			limit = 100
		}
		var queued int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE status='queued'").Scan(&queued) != nil {
			return persistence()
		}
		if queued >= limit {
			return c.NewError(c.CodeRateLimited)
		}
		id, err := s.newID("task")
		if err != nil {
			return persistence()
		}
		now := s.now()
		items := make([]c.TaskItem, len(a.Items))
		refs := map[string]bool{}
		for i, item := range a.Items {
			if item.AccountID != "" && !c.ValidAccountID(item.AccountID) {
				return c.NewError(c.CodeInvalidRequest)
			}
			if a.Kind == c.TaskImport && (item.ClientRef == "" || strings.TrimSpace(item.ClientRef) != item.ClientRef || utf8.RuneCountInString(item.ClientRef) > 128 || refs[item.ClientRef]) {
				return c.NewError(c.CodeInvalidRequest)
			}
			refs[item.ClientRef] = true
			// Only admission identity fields cross into the public projection.
			items[i] = c.TaskItem{ClientRef: item.ClientRef, AccountID: item.AccountID, Status: c.TaskQueued, Stage: "queued"}
		}
		r := c.TaskRecord{Task: c.Task{TaskID: id, Kind: a.Kind, Status: c.TaskQueued, CreatedByKeyID: a.Principal.KeyID, CreatedAt: now, UpdatedAt: now, Items: items}, Revision: 1}
		r.Counts = taskCounts(items)
		b, err = json.Marshal(r.Task)
		if err != nil {
			return persistence()
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO tasks(task_id,key_id,kind,status,created_at,updated_at,revision,projection) VALUES(?,?,?,?,?,?,?,?)", id, r.CreatedByKeyID, r.Kind, r.Status, now.UnixMilli(), now.UnixMilli(), r.Revision, b); err != nil {
			return persistence()
		}
		if err = s.captureTask(ctx, tx, r, a.Capture); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO task_inputs(task_id,input) VALUES(?,?)", id, []byte(a.PrivateInput)); err != nil {
			return persistence()
		}
		result.Accepted = c.TaskAccepted{TaskID: id, Status: c.TaskQueued}
		b, err = json.Marshal(result.Accepted)
		if err != nil {
			return persistence()
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO idempotency(key_id,idempotency_key,digest,task_id,acceptance) VALUES(?,?,?,?,?)", a.Principal.KeyID, a.IdempotencyKey, a.CanonicalDigest[:], id, b); err != nil {
			return persistence()
		}
		return nil
	})
	return result, err
}
func readTask(ctx context.Context, q queryer, id string) (c.TaskRecord, error) {
	var r c.TaskRecord
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT projection,revision,cancel_requested FROM tasks WHERE task_id=?", id).Scan(&b, &r.Revision, &r.CancellationRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return r, c.NewError(c.CodeNotFound)
	}
	if err != nil || json.Unmarshal(b, &r.Task) != nil {
		return r, persistence()
	}
	return r, nil
}
func (s *store) GetTask(ctx context.Context, id string) (c.TaskRecord, error) {
	return readTask(ctx, s.db, id)
}
func (s *store) ListTasks(ctx context.Context, q c.PageQuery) (c.Page[c.Task], error) {
	return page(ctx, s.db, q, "SELECT COUNT(*) FROM tasks", "SELECT projection FROM tasks ORDER BY created_at DESC,task_id DESC LIMIT ? OFFSET ?", decodeJSON[c.Task])
}
func (s *store) writeTask(ctx context.Context, tx *sql.Tx, r c.TaskRecord) error {
	r.Counts = taskCounts(r.Items)
	b, err := json.Marshal(r.Task)
	if err != nil {
		return c.NewError(c.CodeInvalidRequest)
	}
	var finished any
	if r.FinishedAt != nil {
		finished = r.FinishedAt.UnixMilli()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tasks SET status=?,updated_at=?,finished_at=?,revision=?,cancel_requested=?,projection=? WHERE task_id=?", r.Status, r.UpdatedAt.UnixMilli(), finished, r.Revision, r.CancellationRequested, b, r.TaskID); err != nil {
		return persistence()
	}
	if r.Status.Terminal() || r.CancellationRequested {
		if _, err = tx.ExecContext(ctx, "DELETE FROM task_inputs WHERE task_id=?", r.TaskID); err != nil {
			return persistence()
		}
	}
	return nil
}
func taskCounts(items []c.TaskItem) c.TaskCounts {
	n := c.TaskCounts{Total: len(items)}
	for _, i := range items {
		switch i.Status {
		case c.TaskQueued:
			n.Queued++
		case c.TaskRunning:
			n.Running++
		case c.TaskSucceeded:
			n.Succeeded++
		case c.TaskFailed:
			n.Failed++
		case c.TaskCancelled:
			n.Cancelled++
		case c.TaskInterrupted:
			n.Interrupted++
		}
	}
	return n
}
func interrupt(r *c.TaskRecord, status c.TaskState, now c.Timestamp) {
	code := c.CodeInterrupted
	if status == c.TaskCancelled {
		code = c.CodeCancelled
	}
	r.Status = status
	r.Error = c.NewError(code)
	r.UpdatedAt = now
	r.FinishedAt = &now
	r.Revision++
	for i := range r.Items {
		item := &r.Items[i]
		if !item.Status.Terminal() {
			item.Status = status
			item.Stage = string(status)
			item.Progress = 100
			item.Error = c.NewError(code)
		}
	}
	r.Counts = taskCounts(r.Items)
}
func (s *store) ClaimTask(ctx context.Context, id string) (c.TaskWork, error) {
	var work c.TaskWork
	var deferred error
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		r, err := readTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Status != c.TaskQueued {
			return c.NewError(c.CodeInvalidState)
		}
		active, err := keyActive(ctx, tx, r.CreatedByKeyID, s.now().UnixMilli())
		if err != nil {
			return err
		}
		if !active || r.CancellationRequested {
			interrupt(&r, c.TaskCancelled, s.now())
			r.CancellationRequested = true
			if err = s.attachReceipts(ctx, tx, &r); err != nil {
				return err
			}
			deferred = c.NewError(c.CodeCancelled)
			return s.writeTask(ctx, tx, r)
		}
		var executed int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_commits WHERE upload_task_id=?", id).Scan(&executed) != nil {
			return persistence()
		}
		if executed != 0 {
			if err = s.attachReceipts(ctx, tx, &r); err != nil {
				return err
			}
			interrupt(&r, c.TaskInterrupted, s.now())
			deferred = c.NewError(c.CodeInterrupted)
			return s.writeTask(ctx, tx, r)
		}
		var private []byte
		if err = tx.QueryRowContext(ctx, "SELECT input FROM task_inputs WHERE task_id=?", id).Scan(&private); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return persistence()
			}
			deferred = c.NewError(c.CodeInterrupted)
			return s.interruptQueued(ctx, tx, &r)
		}
		if !json.Valid(private) || len(private) > c.MaxBodyBytes || r.StartedAt != nil {
			deferred = c.NewError(c.CodeInterrupted)
			return s.interruptQueued(ctx, tx, &r)
		}
		for _, item := range r.Items {
			if item.Status != c.TaskQueued {
				deferred = c.NewError(c.CodeInterrupted)
				return s.interruptQueued(ctx, tx, &r)
			}
		}
		set, preconditions, protected, err := loadProtection(ctx, tx, r)
		if err != nil {
			return err
		}
		if !protected {
			deferred = c.NewError(c.CodeInterrupted)
			return s.interruptQueued(ctx, tx, &r)
		}
		now := s.now()
		r.Status = c.TaskRunning
		r.StartedAt = &now
		r.UpdatedAt = now
		r.Revision++
		if err = s.writeTask(ctx, tx, r); err != nil {
			return err
		}
		work = c.TaskWork{Record: r, PrivateInput: json.RawMessage(private), PreconditionSet: set, Preconditions: preconditions}
		return nil
	})
	if err == nil && deferred != nil {
		err = deferred
	}
	return work, err
}
func legalTransition(from, to c.TaskState, item bool) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	if from == c.TaskQueued {
		return to == c.TaskRunning || to == c.TaskCancelled || to == c.TaskInterrupted || item && (to == c.TaskSucceeded || to == c.TaskFailed)
	}
	if from == c.TaskRunning {
		return to.Terminal() && (!item || to != c.TaskPartial)
	}
	return false
}
func (s *store) SaveTask(ctx context.Context, r c.TaskRecord, expected uint64) (c.TaskRecord, error) {
	var saved c.TaskRecord
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		old, err := readTask(ctx, tx, r.TaskID)
		if err != nil {
			return err
		}
		if old.Revision != expected {
			return c.NewError(c.CodeVersionConflict)
		}
		if old.Status.Terminal() {
			return c.NewError(c.CodeInvalidState)
		}
		if r.Kind != old.Kind || r.CreatedByKeyID != old.CreatedByKeyID || !r.CreatedAt.Equal(old.CreatedAt.Time) || len(r.Items) != len(old.Items) || !legalTransition(old.Status, r.Status, false) {
			return c.NewError(c.CodeInvalidState)
		}
		for i := range r.Items {
			item, prior := &r.Items[i], old.Items[i]
			if item.ClientRef != prior.ClientRef || !legalTransition(prior.Status, item.Status, true) || item.Progress < prior.Progress || item.Progress > 100 || !validStage(item.Stage) {
				return c.NewError(c.CodeInvalidState)
			}
			if prior.AccountID != "" && item.AccountID != prior.AccountID {
				return c.NewError(c.CodeInvalidState)
			}
			if item.AccountID != "" && !c.ValidAccountID(item.AccountID) {
				return c.NewError(c.CodeInvalidRequest)
			}
			if prior.Index != nil && !reflect.DeepEqual(prior.Index, item.Index) {
				return c.NewError(c.CodeInvalidState)
			}
			if prior.Status.Terminal() && (!reflect.DeepEqual(prior.Result, item.Result) || !reflect.DeepEqual(prior.Error, item.Error) || prior.Progress != item.Progress || prior.Stage != item.Stage) {
				return c.NewError(c.CodeInvalidState)
			}
			if item.Error != nil {
				item.Error = c.PublicError(item.Error)
			}
			if item.Result != nil && (item.Result.Upload == nil) == (item.Result.Verification == nil) {
				return c.NewError(c.CodeInvalidRequest)
			}
			if item.Result != nil && item.Result.Verification != nil {
				v := item.Result.Verification
				if !c.ValidModel(v.Model) || !c.ValidRequestID(v.RequestID) || v.ActualChannel != c.ChannelPlayground || (v.Stage != "connection_verified" && v.Stage != "model_verified") || v.CredentialVersion < 1 || v.StateVersion < 1 || v.CredentialVersion > c.MaxSafeInteger || v.StateVersion > c.MaxSafeInteger || v.AuthIndex < 0 || v.AuthIndex > c.MaxSafeInteger {
					return c.NewError(c.CodeInvalidRequest)
				}
			}
			if item.Result != nil && item.Result.Upload != nil {
				receipt, err := readReceipt(ctx, tx, c.UploadIdentity{TaskID: r.TaskID, ItemIndex: i, Kind: r.Kind})
				if err != nil {
					return err
				}
				if receipt == nil || item.Result.Upload.Versions != receipt.Versions || !item.Result.Upload.Success || item.Result.Upload.Stage != "uploaded" {
					return c.NewError(c.CodeInvalidState)
				}
			}
		}
		if err = s.attachReceipts(ctx, tx, &r); err != nil {
			return err
		}
		for i, item := range r.Items {
			if old.Items[i].AccountID == "" && item.AccountID != "" && item.Upload == nil {
				return c.NewError(c.CodeInvalidState)
			}
			if item.Index != nil && item.Upload == nil {
				a, err := readAccount(ctx, tx, "a.account_id=?", item.AccountID)
				if err != nil || a.Index != *item.Index {
					return c.NewError(c.CodeInvalidState)
				}
			}
		}
		if r.Result != nil {
			for _, id := range r.Result.AccountIDs {
				found := false
				for _, item := range r.Items {
					if item.AccountID == id {
						found = true
						break
					}
				}
				if !found || !c.ValidAccountID(id) {
					return c.NewError(c.CodeInvalidRequest)
				}
			}
		}
		r.CancellationRequested = old.CancellationRequested || r.CancellationRequested
		if r.Error != nil {
			r.Error = c.PublicError(r.Error)
		}
		r.StartedAt = old.StartedAt
		if old.Revision >= c.MaxSafeInteger {
			return c.NewError(c.CodeInvalidState)
		}
		r.Revision = old.Revision + 1
		r.UpdatedAt = s.now()
		r.Counts = taskCounts(r.Items)
		if r.Status.Terminal() {
			for _, item := range r.Items {
				if !item.Status.Terminal() {
					return c.NewError(c.CodeInvalidState)
				}
			}
			n := r.Counts
			outcome := c.TaskFailed
			if n.Succeeded == n.Total {
				outcome = c.TaskSucceeded
			} else if n.Succeeded > 0 {
				outcome = c.TaskPartial
			} else if n.Interrupted > 0 {
				outcome = c.TaskInterrupted
			} else if n.Cancelled > 0 {
				outcome = c.TaskCancelled
			}
			if r.Status != outcome {
				return c.NewError(c.CodeInvalidState)
			}
			now := s.now()
			r.FinishedAt = &now
		} else {
			r.FinishedAt = nil
		}
		if err = s.writeTask(ctx, tx, r); err != nil {
			return err
		}
		saved = r
		return nil
	})
	return saved, err
}
func (s *store) attachReceipts(ctx context.Context, q queryer, r *c.TaskRecord) error {
	for i := range r.Items {
		item := &r.Items[i]
		receipt, err := readReceipt(ctx, q, c.UploadIdentity{TaskID: r.TaskID, ItemIndex: i, Kind: r.Kind})
		if err != nil {
			return err
		}
		if item.Upload != nil && (receipt == nil || !reflect.DeepEqual(*item.Upload, *receipt)) {
			return c.NewError(c.CodeInvalidState)
		}
		if receipt == nil {
			continue
		}
		if item.AccountID != "" && item.AccountID != receipt.AccountID || item.Index != nil && *item.Index != receipt.Index {
			return c.NewError(c.CodeInvalidState)
		}
		item.Upload = receipt
		item.AccountID = receipt.AccountID
		index := receipt.Index
		item.Index = &index
		if item.Result == nil {
			item.Result = &c.ItemResult{Upload: &c.UploadOnlyResult{Success: true, Stage: "uploaded", Versions: receipt.Versions}}
		}
	}
	return nil
}
func (s *store) RequestTaskCancellation(ctx context.Context, id string) (c.TaskRecord, error) {
	var result c.TaskRecord
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		r, err := readTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if !r.Status.Terminal() {
			if err = s.attachReceipts(ctx, tx, &r); err != nil {
				return err
			}
			r.CancellationRequested = true
			if r.Status == c.TaskQueued {
				interrupt(&r, c.TaskCancelled, s.now())
			} else {
				r.Revision++
				r.UpdatedAt = s.now()
			}
			if err = s.writeTask(ctx, tx, r); err != nil {
				return err
			}
		}
		result = r
		return nil
	})
	return result, err
}
func (s *store) cancelForKey(ctx context.Context, tx *sql.Tx, key string) error {
	rows, err := tx.QueryContext(ctx, "SELECT task_id FROM tasks WHERE key_id=? AND status IN ('queued','running') ORDER BY created_at,task_id", key)
	if err != nil {
		return persistence()
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return persistence()
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return persistence()
	}
	for _, id := range ids {
		r, err := readTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = s.attachReceipts(ctx, tx, &r); err != nil {
			return err
		}
		r.CancellationRequested = true
		if r.Status == c.TaskQueued {
			interrupt(&r, c.TaskCancelled, s.now())
		} else {
			r.Revision++
			r.UpdatedAt = s.now()
		}
		if err = s.writeTask(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}
func (s *store) CancelQueuedForKey(ctx context.Context, key string) error {
	return transaction(ctx, s.db, func(tx *sql.Tx) error { return s.cancelForKey(ctx, tx, key) })
}

func (s *store) RecoverTasks(ctx context.Context) ([]string, error) {
	return s.recoverTasks(ctx)
}
func (s *store) Prune(ctx context.Context, cutoff c.Timestamp) error {
	return s.pruneBatches(ctx, cutoff)
}
