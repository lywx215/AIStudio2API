package managementstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func captureMode(kind c.TaskKind) c.TaskCaptureMode {
	if kind == c.TaskImport {
		return c.TaskCaptureNone
	}
	if kind == c.TaskReloadAuth {
		return c.TaskCaptureReloadAuth
	}
	return c.TaskCaptureItems
}
func validateCapture(kind c.TaskKind, items []c.TaskItem, in c.TaskCaptureInstruction) error {
	if in.Mode != captureMode(kind) || in.Expected.Validate(false) != nil {
		return c.NewError(c.CodeInvalidRequest)
	}
	if in.Expected.Credential != nil && (in.Mode != c.TaskCaptureItems || len(items) != 1) {
		return c.NewError(c.CodeInvalidRequest)
	}
	if in.Mode == c.TaskCaptureReloadAuth && (len(items) != 1 || items[0].AccountID != "") {
		return c.NewError(c.CodeInvalidRequest)
	}
	for _, item := range items {
		if in.Mode == c.TaskCaptureItems && !c.ValidAccountID(item.AccountID) || in.Mode == c.TaskCaptureNone && item.AccountID != "" {
			return c.NewError(c.CodeInvalidRequest)
		}
	}
	return nil
}
func cloneExpected(v c.ExpectedVersions) c.ExpectedVersions {
	if v.Credential == nil || v.State == nil {
		return c.ExpectedVersions{}
	}
	cv, sv := *v.Credential, *v.State
	return c.ExpectedVersions{Credential: &cv, State: &sv}
}
func insertPrecondition(ctx context.Context, tx *sql.Tx, id string, p c.TaskPrecondition) error {
	var cv, sv, ecv, esv any
	if p.SnapshotKnown && !p.Missing {
		cv = p.CredentialVersion
		sv = p.StateVersion
	}
	if p.ClientExpected.Credential != nil {
		ecv = *p.ClientExpected.Credential
		esv = *p.ClientExpected.State
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO task_preconditions(task_id,ordinal,item_index,account_id,snapshot_known,missing,
 credential_version,state_version,client_expected_credential_version,client_expected_state_version) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, p.Ordinal, p.ItemIndex, p.AccountID, p.SnapshotKnown, p.Missing, cv, sv, ecv, esv)
	if err != nil {
		return persistence()
	}
	return nil
}
func insertPreconditionSet(ctx context.Context, tx *sql.Tx, id string, set c.TaskPreconditionSet) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO task_precondition_sets(task_id,format_version,mode,target_count) VALUES(?,?,?,?)", id, set.FormatVersion, set.Mode, set.TargetCount)
	if err != nil {
		return persistence()
	}
	return nil
}
func (s *store) captureTask(ctx context.Context, tx *sql.Tx, r c.TaskRecord, in c.TaskCaptureInstruction) error {
	set := c.TaskPreconditionSet{FormatVersion: c.TaskPreconditionFormatVersion, Mode: in.Mode}
	if in.Mode == c.TaskCaptureItems {
		for i, item := range r.Items {
			p := c.TaskPrecondition{Ordinal: int64(i), ItemIndex: i, AccountID: item.AccountID, SnapshotKnown: true, ClientExpected: cloneExpected(in.Expected)}
			a, err := readAccount(ctx, tx, "a.account_id=?", item.AccountID)
			if isCode(err, c.CodeNotFound) || err == nil && a.Deleted {
				p.Missing = true
			} else if err != nil {
				return err
			} else {
				p.Versions = a.Versions
			}
			if err := insertPrecondition(ctx, tx, r.TaskID, p); err != nil {
				return err
			}
		}
		set.TargetCount = len(r.Items)
	} else if in.Mode == c.TaskCaptureReloadAuth {
		// The complete scan uses bounded pages, all in the admission snapshot. Even
		// disabled accounts are targets; archived/deleted accounts are excluded now.
		var after int64
		for {
			rows, err := tx.QueryContext(ctx, "SELECT record FROM accounts WHERE deleted=0 AND idx>? ORDER BY idx LIMIT ?", after, c.MaxPageLimit)
			if err != nil {
				return persistence()
			}
			batch := []c.AccountRecord{}
			for rows.Next() {
				d, err := decodeJSON[diskAccount](rows)
				if err != nil {
					rows.Close()
					return persistence()
				}
				batch = append(batch, d.unpack())
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return persistence()
			}
			if len(batch) == 0 {
				break
			}
			for _, a := range batch {
				after = a.Index
				if a.Archived {
					continue
				}
				p := c.TaskPrecondition{Ordinal: int64(set.TargetCount), ItemIndex: 0, AccountID: a.AccountID, SnapshotKnown: true, Versions: a.Versions}
				if err := insertPrecondition(ctx, tx, r.TaskID, p); err != nil {
					return err
				}
				set.TargetCount++
			}
			if err := s.checkpoint("capture-page"); err != nil {
				return err
			}
			if len(batch) < c.MaxPageLimit {
				break
			}
		}
	}
	if err := insertPreconditionSet(ctx, tx, r.TaskID, set); err != nil {
		return err
	}
	return s.checkpoint("capture-complete")
}

func readPreconditionSet(ctx context.Context, q queryer, id string) (*c.TaskPreconditionSet, error) {
	var set c.TaskPreconditionSet
	err := q.QueryRowContext(ctx, "SELECT format_version,mode,target_count FROM task_precondition_sets WHERE task_id=?", id).Scan(&set.FormatVersion, &set.Mode, &set.TargetCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, persistence()
	}
	return &set, nil
}
func readPreconditionPage(ctx context.Context, q queryer, id string, offset, limit int) ([]c.TaskPrecondition, error) {
	result := []c.TaskPrecondition{}
	rows, err := q.QueryContext(ctx, `SELECT ordinal,item_index,account_id,snapshot_known,missing,credential_version,state_version,
 client_expected_credential_version,client_expected_state_version FROM task_preconditions WHERE task_id=? ORDER BY ordinal LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return result, persistence()
	}
	defer rows.Close()
	for rows.Next() {
		var p c.TaskPrecondition
		var cv, sv, ecv, esv sql.NullInt64
		if err = rows.Scan(&p.Ordinal, &p.ItemIndex, &p.AccountID, &p.SnapshotKnown, &p.Missing, &cv, &sv, &ecv, &esv); err != nil {
			return result, persistence()
		}
		if cv.Valid != sv.Valid || ecv.Valid != esv.Valid {
			return result, c.NewError(c.CodeInvalidState)
		}
		if cv.Valid {
			p.CredentialVersion = uint64(cv.Int64)
			p.StateVersion = uint64(sv.Int64)
		}
		if ecv.Valid {
			credential, state := uint64(ecv.Int64), uint64(esv.Int64)
			p.ClientExpected = c.ExpectedVersions{Credential: &credential, State: &state}
		}
		result = append(result, p)
	}
	if rows.Err() != nil {
		return result, persistence()
	}
	return result, nil
}
func validSet(r c.TaskRecord, set *c.TaskPreconditionSet) bool {
	if !validKind(r.Kind) || len(r.Items) < 1 || len(r.Items) > c.MaxBatchItems || set == nil || set.FormatVersion != c.TaskPreconditionFormatVersion || set.Mode != captureMode(r.Kind) || set.TargetCount < 0 {
		return false
	}
	switch set.Mode {
	case c.TaskCaptureNone:
		return set.TargetCount == 0
	case c.TaskCaptureItems:
		return len(r.Items) > 0 && len(r.Items) <= c.MaxBatchItems && set.TargetCount == len(r.Items)
	case c.TaskCaptureReloadAuth:
		return len(r.Items) == 1 && r.Items[0].AccountID == ""
	}
	return false
}
func validPrecondition(r c.TaskRecord, set *c.TaskPreconditionSet, p c.TaskPrecondition, ordinal int) bool {
	if p.Ordinal != int64(ordinal) || !c.ValidAccountID(p.AccountID) || p.ClientExpected.Validate(false) != nil {
		return false
	}
	if set.Mode == c.TaskCaptureItems {
		if p.ItemIndex != ordinal || ordinal >= len(r.Items) || p.AccountID != r.Items[ordinal].AccountID {
			return false
		}
	} else if set.Mode == c.TaskCaptureReloadAuth {
		if p.ItemIndex != 0 || !p.SnapshotKnown || p.Missing || p.ClientExpected.Credential != nil {
			return false
		}
	} else {
		return false
	}
	if p.ClientExpected.Credential != nil && (set.Mode != c.TaskCaptureItems || set.TargetCount != 1) {
		return false
	}
	if p.SnapshotKnown {
		if p.Missing {
			return p.Versions == (c.Versions{})
		}
		return p.CredentialVersion >= 1 && p.StateVersion >= 1 && p.CredentialVersion <= c.MaxSafeInteger && p.StateVersion <= c.MaxSafeInteger
	}
	return r.Kind == c.TaskReplace && !p.Missing && p.Versions == (c.Versions{}) && p.ClientExpected.Validate(true) == nil
}

// Protection is validated independently of today's account rows. In particular,
// missing targets and old explicit client versions must never be recaptured.
func loadProtection(ctx context.Context, q queryer, r c.TaskRecord) (*c.TaskPreconditionSet, []c.TaskPrecondition, bool, error) {
	set, err := readPreconditionSet(ctx, q, r.TaskID)
	if err != nil {
		return nil, nil, false, err
	}
	if !validSet(r, set) {
		return set, nil, false, nil
	}
	var actual int
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_preconditions WHERE task_id=?", r.TaskID).Scan(&actual); err != nil {
		return set, nil, false, persistence()
	}
	if actual != set.TargetCount {
		return set, nil, false, nil
	}
	result := []c.TaskPrecondition{}
	for offset := 0; offset < set.TargetCount; offset += c.MaxPageLimit {
		batch, err := readPreconditionPage(ctx, q, r.TaskID, offset, c.MaxPageLimit)
		if isCode(err, c.CodeInvalidState) {
			return set, nil, false, nil
		}
		if err != nil {
			return set, nil, false, err
		}
		expected := min(c.MaxPageLimit, set.TargetCount-offset)
		if len(batch) != expected {
			return set, nil, false, nil
		}
		for i, p := range batch {
			if !validPrecondition(r, set, p, offset+i) {
				return set, nil, false, nil
			}
		}
		if set.Mode != c.TaskCaptureReloadAuth {
			result = append(result, batch...)
		}
	}
	return set, result, true, nil
}

// Legacy compatibility is startup-only and has no live account read. An explicit
// client pair is historical protection, never evidence of a current snapshot.
func (s *store) protectLegacy(ctx context.Context, tx *sql.Tx, r c.TaskRecord, private []byte) (bool, error) {
	set, err := readPreconditionSet(ctx, tx, r.TaskID)
	if err != nil {
		return false, err
	}
	if set != nil {
		return false, nil
	}
	var rows int
	if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_preconditions WHERE task_id=?", r.TaskID).Scan(&rows) != nil {
		return false, persistence()
	}
	if rows != 0 {
		return false, nil
	}
	var legacy bool
	if tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_legacy_queue WHERE task_id=?)", r.TaskID).Scan(&legacy) != nil {
		return false, persistence()
	}
	if !legacy {
		return false, nil
	}
	set = &c.TaskPreconditionSet{FormatVersion: c.TaskPreconditionFormatVersion, Mode: captureMode(r.Kind)}
	switch r.Kind {
	case c.TaskImport:
	case c.TaskReplace:
		if len(r.Items) != 1 || !c.ValidAccountID(r.Items[0].AccountID) {
			return false, nil
		}
		var input struct {
			Expected c.ExpectedVersions `json:"expected"`
		}
		if json.Unmarshal(private, &input) != nil || input.Expected.Validate(true) != nil {
			return false, nil
		}
		p := c.TaskPrecondition{Ordinal: 0, ItemIndex: 0, AccountID: r.Items[0].AccountID, ClientExpected: cloneExpected(input.Expected)}
		if err := insertPrecondition(ctx, tx, r.TaskID, p); err != nil {
			return false, err
		}
		set.TargetCount = 1
	default:
		return false, nil
	}
	if err := insertPreconditionSet(ctx, tx, r.TaskID, *set); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_legacy_queue WHERE task_id=?", r.TaskID); err != nil {
		return false, persistence()
	}
	if err := s.checkpoint("legacy-protection-written"); err != nil {
		return false, err
	}
	return true, nil
}

func (s *store) ListTaskPreconditions(ctx context.Context, id string, q c.PageQuery) (c.Page[c.TaskPrecondition], error) {
	result := c.Page[c.TaskPrecondition]{Items: []c.TaskPrecondition{}, Offset: q.Offset, Limit: q.Limit}
	if err := q.Validate(); err != nil {
		return result, err
	}
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		r, err := readTask(ctx, tx, id)
		if err != nil {
			return err
		}
		set, err := readPreconditionSet(ctx, tx, id)
		if err != nil {
			return err
		}
		if !validSet(r, set) {
			return c.NewError(c.CodeInvalidState)
		}
		result.Total = set.TargetCount
		items, err := readPreconditionPage(ctx, tx, id, q.Offset, q.Limit)
		if err != nil {
			return err
		}
		expected := max(0, min(q.Limit, set.TargetCount-q.Offset))
		if len(items) != expected {
			return c.NewError(c.CodeInvalidState)
		}
		for i, p := range items {
			if !validPrecondition(r, set, p, q.Offset+i) {
				return c.NewError(c.CodeInvalidState)
			}
		}
		result.Items = items
		return nil
	})
	return result, err
}
func (s *store) ListQueuedTasks(ctx context.Context, q c.QueuedTaskQuery) (c.QueuedTaskPage, error) {
	result := c.QueuedTaskPage{Items: []c.QueuedTaskRef{}}
	if err := q.Validate(); err != nil {
		return result, err
	}
	statement := "SELECT task_id,created_at FROM tasks WHERE status='queued'"
	args := []any{}
	if q.After != nil {
		statement += " AND (created_at,task_id)>(?,?)"
		args = append(args, q.After.CreatedAt.UnixMilli(), q.After.TaskID)
	}
	statement += " ORDER BY created_at,task_id LIMIT ?"
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return result, persistence()
	}
	defer rows.Close()
	for rows.Next() {
		var item c.QueuedTaskRef
		var created int64
		if rows.Scan(&item.TaskID, &created) != nil {
			return result, persistence()
		}
		item.CreatedAt = c.NewTimestamp(time.UnixMilli(created))
		result.Items = append(result.Items, item)
	}
	if rows.Err() != nil {
		return result, persistence()
	}
	if len(result.Items) > q.Limit {
		result.Items = result.Items[:q.Limit]
		last := result.Items[len(result.Items)-1]
		result.Next = &last
	}
	return result, nil
}
