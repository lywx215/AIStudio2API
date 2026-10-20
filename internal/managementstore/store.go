// Package managementstore persists process-scoped management metadata. Credential
// files belong to the account participant; this package never opens those files.
package managementstore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type store struct {
	db          *sql.DB
	clock       c.Clock
	newID       c.IDGenerator
	accountGate chan struct{}
	// Tests use this private seam to stop at durable crash boundaries.
	fault func(string) error
}

var _ c.Store = (*store)(nil)

func Open(ctx context.Context, options c.StoreOptions) (c.Store, error) {
	if strings.TrimSpace(options.Path) == "" {
		return nil, c.NewError(c.CodeInvalidRequest)
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, persistence()
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, persistence()
	}
	params := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)", "secure_delete(ON)"}}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, persistence()
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &store{db: db, clock: options.Clock, newID: options.NewID, accountGate: make(chan struct{}, 1)}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.newID == nil {
		s.newID = c.NewID
	}
	err = s.migrate(ctx)
	if err != nil {
		db.Close()
		return nil, persistence()
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, persistence()
	}
	return s, nil
}

func (s *store) Close() error {
	if err := s.db.Close(); err != nil {
		return persistence()
	}
	return nil
}
func persistence() error          { return c.NewError(c.CodePersistenceError) }
func (s *store) now() c.Timestamp { return c.NewTimestamp(s.clock()) }
func (s *store) checkpoint(point string) error {
	if s.fault != nil {
		return s.fault(point)
	}
	return nil
}
func (s *store) lockAccounts(ctx context.Context) error {
	select {
	case s.accountGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *store) unlockAccounts() { <-s.accountGate }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func transaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return persistence()
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return persistence()
	}
	return nil
}

func page[T any](ctx context.Context, db *sql.DB, q c.PageQuery, countSQL, rowsSQL string, decode func(*sql.Rows) (T, error)) (c.Page[T], error) {
	result := c.Page[T]{Items: []T{}, Offset: q.Offset, Limit: q.Limit}
	if err := q.Validate(); err != nil {
		return result, err
	}
	err := transaction(ctx, db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, countSQL).Scan(&result.Total); err != nil {
			return persistence()
		}
		rows, err := tx.QueryContext(ctx, rowsSQL, q.Limit, q.Offset)
		if err != nil {
			return persistence()
		}
		defer rows.Close()
		for rows.Next() {
			item, err := decode(rows)
			if err != nil {
				return persistence()
			}
			result.Items = append(result.Items, item)
		}
		if rows.Err() != nil {
			return persistence()
		}
		return nil
	})
	return result, err
}

func decodeJSON[T any](rows *sql.Rows) (T, error) {
	var result T
	var b []byte
	if err := rows.Scan(&b); err != nil {
		return result, err
	}
	err := json.Unmarshal(b, &result)
	return result, err
}

func normalizedNative(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
