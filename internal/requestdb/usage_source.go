package requestdb

import "database/sql"

// addUsageSource preserves historical counts as unknown-source records. It
// never relabels old usage as native, estimated, or complete.
func addUsageSource(db *sql.DB) error {
	for _, column := range []struct{ name, definition string }{
		{"usage_source", "TEXT NOT NULL DEFAULT ''"},
		{"usage_incomplete", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var exists bool
		if err := db.QueryRow(`SELECT COUNT(*) > 0 FROM pragma_table_info('requests') WHERE name = ?`, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(`ALTER TABLE requests ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}
