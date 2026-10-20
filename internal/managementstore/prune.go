package managementstore

import (
	"context"
	"database/sql"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Each transaction touches at most one bounded class of eligible rows. Large
// reload-auth snapshots are removed in child batches before deleting the task,
// so a cascade cannot turn one task deletion into an unbounded transaction.
func (s *store) pruneBatches(ctx context.Context, cutoff c.Timestamp) error {
	ctx, cancel := context.WithTimeout(ctx, c.MaintenanceTimeout)
	defer cancel()
	maximum := s.now().Add(-c.Retention)
	if cutoff.After(maximum) {
		cutoff = c.NewTimestamp(maximum)
	}
	batches := []struct {
		point, statement string
		args             []any
	}{
		{"prune-preconditions", `DELETE FROM task_preconditions WHERE rowid IN (
   SELECT p.rowid FROM tasks t JOIN task_preconditions p ON p.task_id=t.task_id
   WHERE t.finished_at<? AND t.status NOT IN ('queued','running')
   ORDER BY t.finished_at,t.task_id,p.ordinal LIMIT ?)`, []any{cutoff.UnixMilli(), c.PruneBatchRows}},
		{"prune-tasks", `DELETE FROM tasks WHERE task_id IN (
   SELECT t.task_id FROM tasks t WHERE t.finished_at<? AND t.status NOT IN ('queued','running')
   AND NOT EXISTS(SELECT 1 FROM task_preconditions p WHERE p.task_id=t.task_id)
   ORDER BY t.finished_at,t.task_id LIMIT ?)`, []any{cutoff.UnixMilli(), c.PruneBatchRows}},
		{"prune-inputs", `DELETE FROM task_inputs WHERE task_id IN (
   SELECT i.task_id FROM task_inputs i JOIN tasks t ON t.task_id=i.task_id
   WHERE t.status NOT IN ('queued','running') OR t.cancel_requested=1
   ORDER BY t.task_id LIMIT ?)`, []any{c.PruneBatchRows}},
		{"prune-audit", `DELETE FROM audit WHERE audit_id IN (
   SELECT audit_id FROM audit WHERE timestamp<? ORDER BY timestamp,audit_id LIMIT ?)`, []any{cutoff.UnixMilli(), c.PruneBatchRows}},
	}
	for {
		var progress int64
		for _, batch := range batches {
			if err := ctx.Err(); err != nil {
				return err
			}
			var affected int64
			err := transaction(ctx, s.db, func(tx *sql.Tx) error {
				result, err := tx.ExecContext(ctx, batch.statement, batch.args...)
				if err != nil {
					return persistence()
				}
				affected, err = result.RowsAffected()
				if err != nil || affected > c.PruneBatchRows {
					return persistence()
				}
				return nil
			})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			progress += affected
			if err = s.checkpoint(batch.point); err != nil {
				return err
			}
		}
		if progress == 0 {
			return nil
		}
	}
}
