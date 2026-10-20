PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS identities (
 idx INTEGER PRIMARY KEY AUTOINCREMENT CHECK(idx BETWEEN 1 AND 9007199254740991),
 account_id TEXT NOT NULL UNIQUE, native_id TEXT NOT NULL UNIQUE
);
CREATE TRIGGER IF NOT EXISTS immutable_identity_update BEFORE UPDATE ON identities
 BEGIN SELECT RAISE(ABORT, 'immutable identity'); END;
CREATE TRIGGER IF NOT EXISTS immutable_identity_delete BEFORE DELETE ON identities
 BEGIN SELECT RAISE(ABORT, 'immutable identity'); END;
CREATE TABLE IF NOT EXISTS accounts (
 account_id TEXT PRIMARY KEY REFERENCES identities(account_id), idx INTEGER NOT NULL UNIQUE,
 deleted INTEGER NOT NULL, record BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS account_commits (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, operation_id TEXT NOT NULL UNIQUE,
 account_id TEXT NOT NULL, phase TEXT NOT NULL, completed INTEGER NOT NULL DEFAULT 0,
 upload_task_id TEXT, upload_item_index INTEGER,
 record BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS pending_account_commits ON account_commits(account_id, completed);
CREATE INDEX IF NOT EXISTS commits_upload_task ON account_commits(upload_task_id);
CREATE TABLE IF NOT EXISTS upload_receipts (
 task_id TEXT NOT NULL, item_index INTEGER NOT NULL, kind TEXT NOT NULL,
 operation_id TEXT NOT NULL UNIQUE, receipt BLOB NOT NULL,
 PRIMARY KEY(task_id, item_index, kind)
);
CREATE TRIGGER IF NOT EXISTS immutable_receipt_update BEFORE UPDATE ON upload_receipts
 BEGIN SELECT RAISE(ABORT, 'immutable receipt'); END;
CREATE TRIGGER IF NOT EXISTS immutable_receipt_delete BEFORE DELETE ON upload_receipts
 BEGIN SELECT RAISE(ABORT, 'immutable receipt'); END;
CREATE TABLE IF NOT EXISTS management_keys (
 key_id TEXT PRIMARY KEY, digest BLOB NOT NULL UNIQUE CHECK(length(digest)=32),
 created_at INTEGER NOT NULL, expires_at INTEGER, revoked_at INTEGER, metadata BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
 task_id TEXT PRIMARY KEY, key_id TEXT NOT NULL REFERENCES management_keys(key_id),
 kind TEXT NOT NULL, status TEXT NOT NULL, created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, finished_at INTEGER, revision INTEGER NOT NULL,
 cancel_requested INTEGER NOT NULL DEFAULT 0, projection BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_page ON tasks(created_at DESC, task_id DESC);
CREATE INDEX IF NOT EXISTS tasks_key_status ON tasks(key_id, status);
CREATE TABLE IF NOT EXISTS task_inputs (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE, input BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS idempotency (
 key_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, digest BLOB NOT NULL,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(task_id) ON DELETE CASCADE, acceptance BLOB NOT NULL,
 PRIMARY KEY(key_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS audit (
 audit_id TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, entry BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_page ON audit(timestamp DESC, audit_id DESC);
PRAGMA user_version = 1;
