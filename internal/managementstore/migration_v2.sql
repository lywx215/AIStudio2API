CREATE TABLE IF NOT EXISTS task_precondition_sets (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE,
 format_version INTEGER NOT NULL CHECK(format_version=1),
 mode TEXT NOT NULL CHECK(mode IN ('none','items','reload-auth')),
 target_count INTEGER NOT NULL CHECK(target_count BETWEEN 0 AND 9007199254740991)
);
-- Only v1 migration populates this provenance table. A new v2 task whose
-- manifest/rows are lost must never be mistaken for an explicit-CAS v1 task.
CREATE TABLE IF NOT EXISTS task_legacy_queue (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS task_preconditions (
 task_id TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 9007199254740991),
 item_index INTEGER NOT NULL CHECK(item_index BETWEEN 0 AND 99),
 account_id TEXT NOT NULL,
 snapshot_known INTEGER NOT NULL CHECK(snapshot_known IN (0,1)),
 missing INTEGER NOT NULL CHECK(missing IN (0,1)),
 credential_version INTEGER, state_version INTEGER,
 client_expected_credential_version INTEGER, client_expected_state_version INTEGER,
 PRIMARY KEY(task_id,ordinal), UNIQUE(task_id,item_index,account_id),
 CHECK((credential_version IS NULL AND state_version IS NULL) OR
  (credential_version IS NOT NULL AND state_version IS NOT NULL AND
   credential_version BETWEEN 1 AND 9007199254740991 AND state_version BETWEEN 1 AND 9007199254740991)),
 CHECK((client_expected_credential_version IS NULL AND client_expected_state_version IS NULL) OR
  (client_expected_credential_version IS NOT NULL AND client_expected_state_version IS NOT NULL AND
   client_expected_credential_version BETWEEN 1 AND 9007199254740991 AND client_expected_state_version BETWEEN 1 AND 9007199254740991)),
 CHECK((snapshot_known=1 AND missing=0 AND credential_version IS NOT NULL) OR
  (snapshot_known=1 AND missing=1 AND credential_version IS NULL) OR
  (snapshot_known=0 AND missing=0 AND credential_version IS NULL AND client_expected_credential_version IS NOT NULL))
);
CREATE TRIGGER IF NOT EXISTS immutable_task_precondition_update BEFORE UPDATE ON task_preconditions
 BEGIN SELECT RAISE(ABORT, 'immutable task precondition'); END;
CREATE TRIGGER IF NOT EXISTS immutable_task_precondition_set_update BEFORE UPDATE ON task_precondition_sets
 BEGIN SELECT RAISE(ABORT, 'immutable task precondition set'); END;
CREATE INDEX IF NOT EXISTS tasks_queued_cursor ON tasks(status,created_at,task_id);
CREATE INDEX IF NOT EXISTS tasks_retention ON tasks(finished_at,task_id);
PRAGMA user_version = 2;
