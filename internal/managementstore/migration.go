package managementstore

import (
	"context"
	"database/sql"
	_ "embed"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

//go:embed migration_v2.sql
var schemaV2 string

// Only DDL runs here. Legacy execution decisions belong to startup recovery,
// after account journals have recovered; no live version is recaptured here.
func (s *store) migrate(ctx context.Context) error {
	return transaction(ctx, s.db, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return persistence()
		}
		if version < 0 || version > c.ManagementSchemaVersion {
			return persistence()
		}
		if version == c.ManagementSchemaVersion {
			return nil
		}
		if version == 0 {
			if _, err := tx.ExecContext(ctx, schema); err != nil {
				return persistence()
			}
		}
		if _, err := tx.ExecContext(ctx, schemaV2); err != nil {
			return persistence()
		}
		if version == 1 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_legacy_queue(task_id) SELECT task_id FROM tasks WHERE status='queued'"); err != nil {
				return persistence()
			}
		}
		return s.checkpoint("migration-before-commit")
	})
}
