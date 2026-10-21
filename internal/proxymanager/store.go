package proxymanager

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS proxy_schema(version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY,name TEXT NOT NULL,kind TEXT NOT NULL,protocol TEXT NOT NULL,url TEXT NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL,refreshed_at TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS proxies(id TEXT PRIMARY KEY,url TEXT NOT NULL UNIQUE,protocol TEXT NOT NULL,endpoint TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,revision INTEGER NOT NULL DEFAULT 1,state TEXT NOT NULL DEFAULT 'untested',exit_ip TEXT NOT NULL DEFAULT '',latency REAL NOT NULL DEFAULT 0,studio_latency REAL NOT NULL DEFAULT 0,checked_at TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS memberships(source_id TEXT NOT NULL REFERENCES sources(id),proxy_id TEXT NOT NULL REFERENCES proxies(id),present INTEGER NOT NULL,PRIMARY KEY(source_id,proxy_id));
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,kind TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL,cancelled INTEGER NOT NULL DEFAULT 0,concurrency INTEGER NOT NULL,idempotency_key TEXT UNIQUE,fingerprint TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS job_items(job_id TEXT NOT NULL REFERENCES jobs(id),idx INTEGER NOT NULL,input BLOB NOT NULL,result BLOB NOT NULL,status TEXT NOT NULL,PRIMARY KEY(job_id,idx));
CREATE INDEX IF NOT EXISTS proxy_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS proxy_memberships_present ON memberships(proxy_id,present);
INSERT OR IGNORE INTO proxy_schema VALUES(1);`

type Options struct {
	Path     string
	Accounts AccountBackend
	Download func(context.Context, string) ([]byte, error)
	Probe    func(context.Context, string) ProbeResult
}
type Service struct {
	db       *sql.DB
	accounts AccountBackend
	download func(context.Context, string) ([]byte, error)
	probe    func(context.Context, string) ProbeResult
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex // admission, source CAS and stop/close; never held during network/proxy application
	closed   bool
	running  map[string]context.CancelFunc
}

func Open(ctx context.Context, options Options) (*Service, error) {
	path, err := filepath.Abs(options.Path)
	if err != nil || options.Path == "" {
		return nil, fail("INVALID_PATH")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	// Create with private permissions before SQLite creates its journal files.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	q := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(DELETE)", "synchronous(FULL)", "secure_delete(ON)"}}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err == nil {
		_, err = tx.ExecContext(ctx, schema)
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if err != nil {
		db.Close()
		return nil, fail("STORAGE_FAILED")
	}
	var version int
	if err = db.QueryRowContext(ctx, "SELECT max(version) FROM proxy_schema").Scan(&version); err != nil || version != 1 {
		db.Close()
		return nil, fail("UNSUPPORTED_SCHEMA")
	}
	life, cancel := context.WithCancel(ctx)
	s := &Service{db: db, accounts: options.Accounts, ctx: life, cancel: cancel, download: options.Download, probe: options.Probe, running: map[string]context.CancelFunc{}}
	if s.download == nil {
		s.download = Download
	}
	if s.probe == nil {
		s.probe = Probe
	}
	if err = s.recover(ctx); err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}
func transaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail("STORAGE_FAILED")
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return fail("STORAGE_FAILED")
	}
	return nil
}
