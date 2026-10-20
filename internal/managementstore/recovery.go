package managementstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *store) interruptQueued(ctx context.Context, tx *sql.Tx, r *c.TaskRecord) error {
	if err := s.attachReceipts(ctx, tx, r); err != nil {
		return err
	}
	interrupt(r, c.TaskInterrupted, s.now())
	return s.writeTask(ctx, tx, *r)
}
func (s *store) recoverTasks(ctx context.Context) ([]string, error) {
	safe := []string{}
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		var pending int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_commits WHERE completed=0").Scan(&pending) != nil {
			return persistence()
		}
		if pending != 0 {
			return c.NewError(c.CodeInvalidState)
		}
		var afterTime int64
		var afterID string
		first := true
		for {
			statement := "SELECT task_id,created_at FROM tasks"
			args := []any{}
			if !first {
				statement += " WHERE (created_at,task_id)>(?,?)"
				args = append(args, afterTime, afterID)
			}
			statement += " ORDER BY created_at,task_id LIMIT ?"
			args = append(args, c.MaxPageLimit)
			rows, err := tx.QueryContext(ctx, statement, args...)
			if err != nil {
				return persistence()
			}
			type ref struct {
				id      string
				created int64
			}
			batch := []ref{}
			for rows.Next() {
				var r ref
				if rows.Scan(&r.id, &r.created) != nil {
					rows.Close()
					return persistence()
				}
				batch = append(batch, r)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return persistence()
			}
			if len(batch) == 0 {
				break
			}
			first = false
			for _, ref := range batch {
				afterTime, afterID = ref.created, ref.id
				r, err := readTask(ctx, tx, ref.id)
				if err != nil {
					return err
				}
				before, err := json.Marshal(r.Task)
				if err != nil {
					return persistence()
				}
				revision, cancelled := r.Revision, r.CancellationRequested
				if err = s.attachReceipts(ctx, tx, &r); err != nil {
					return err
				}
				if r.Status == c.TaskRunning {
					interrupt(&r, c.TaskInterrupted, s.now())
				} else if r.Status == c.TaskQueued {
					if err = s.recoverQueued(ctx, tx, &r, &safe); err != nil {
						return err
					}
				}
				after, err := json.Marshal(r.Task)
				if err != nil {
					return persistence()
				}
				if !bytes.Equal(before, after) || cancelled != r.CancellationRequested {
					r.Revision = revision + 1
					r.UpdatedAt = s.now()
					if err = s.writeTask(ctx, tx, r); err != nil {
						return err
					}
				} else if r.Status.Terminal() || r.CancellationRequested {
					// Input cleanup must not bump an unchanged terminal task's public history.
					if _, err = tx.ExecContext(ctx, "DELETE FROM task_inputs WHERE task_id=?", r.TaskID); err != nil {
						return persistence()
					}
				}
			}
			if len(batch) < c.MaxPageLimit {
				break
			}
		}
		return nil
	})
	return safe, err
}
func (s *store) recoverQueued(ctx context.Context, tx *sql.Tx, r *c.TaskRecord, safe *[]string) error {
	var executed bool
	if tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM account_commits WHERE upload_task_id=?)", r.TaskID).Scan(&executed) != nil {
		return persistence()
	}
	evidence := executed || r.StartedAt != nil
	for _, item := range r.Items {
		evidence = evidence || item.Upload != nil || item.Status != c.TaskQueued
	}
	if evidence {
		interrupt(r, c.TaskInterrupted, s.now())
		return nil
	}
	active, err := keyActive(ctx, tx, r.CreatedByKeyID, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if !active || r.CancellationRequested {
		interrupt(r, c.TaskCancelled, s.now())
		r.CancellationRequested = true
		return nil
	}
	var private []byte
	err = tx.QueryRowContext(ctx, "SELECT input FROM task_inputs WHERE task_id=?", r.TaskID).Scan(&private)
	if errors.Is(err, sql.ErrNoRows) {
		interrupt(r, c.TaskInterrupted, s.now())
		return nil
	}
	if err != nil {
		return persistence()
	}
	if !json.Valid(private) || len(private) > c.MaxBodyBytes {
		interrupt(r, c.TaskInterrupted, s.now())
		return nil
	}
	_, _, protected, err := loadProtection(ctx, tx, *r)
	if err != nil {
		return err
	}
	if !protected {
		repaired, err := s.protectLegacy(ctx, tx, *r, private)
		if err != nil {
			return err
		}
		if repaired {
			_, _, protected, err = loadProtection(ctx, tx, *r)
			if err != nil {
				return err
			}
		}
	}
	if protected {
		*safe = append(*safe, r.TaskID)
	} else {
		interrupt(r, c.TaskInterrupted, s.now())
	}
	return nil
}
