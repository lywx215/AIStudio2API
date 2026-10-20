package managementstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *store) CreateKey(ctx context.Context, in c.KeyCreateRequest) (c.KeyCreated, error) {
	var result c.KeyCreated
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 128 || len(in.Scopes) == 0 {
		return result, c.NewError(c.CodeInvalidRequest)
	}
	allowed := map[c.Scope]bool{}
	for _, scope := range c.Scopes() {
		allowed[scope] = true
	}
	seen := map[c.Scope]bool{}
	for _, scope := range in.Scopes {
		if !allowed[scope] || seen[scope] {
			return result, c.NewError(c.CodeInvalidRequest)
		}
		seen[scope] = true
	}
	now := s.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now.Time) {
		return result, c.NewError(c.CodeInvalidRequest)
	}
	id, err := s.newID("mkey")
	if err != nil {
		return result, persistence()
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return result, persistence()
	}
	token := "mgmt_" + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(token))
	meta := c.ManagementKey{ID: id, Name: strings.TrimSpace(in.Name), Scopes: append([]c.Scope{}, in.Scopes...), CreatedAt: now, ExpiresAt: in.ExpiresAt}
	b, err := json.Marshal(meta)
	if err != nil {
		return result, c.NewError(c.CodeInvalidRequest)
	}
	var expires any
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UnixMilli()
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO management_keys(key_id,digest,created_at,expires_at,metadata) VALUES(?,?,?,?,?)", id, digest[:], now.UnixMilli(), expires, b); err != nil {
		return result, persistence()
	}
	return c.KeyCreated{Key: meta, Token: token}, nil
}
func keyActive(ctx context.Context, q queryer, id string, now int64) (bool, error) {
	var live bool
	err := q.QueryRowContext(ctx, "SELECT revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?) FROM management_keys WHERE key_id=?", now, id).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, persistence()
	}
	return live, nil
}
func (s *store) IsKeyActive(ctx context.Context, id string) (bool, error) {
	return keyActive(ctx, s.db, id, s.now().UnixMilli())
}
func (s *store) AuthenticateKey(ctx context.Context, token string) (c.Principal, error) {
	digest := sha256.Sum256([]byte(token))
	var stored []byte
	var b []byte
	var live bool
	err := s.db.QueryRowContext(ctx, "SELECT digest,metadata,revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?) FROM management_keys WHERE digest=?", s.now().UnixMilli(), digest[:]).Scan(&stored, &b, &live)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c.Principal{}, persistence()
	}
	// A same-length dummy comparison is also performed for an unknown token.
	var comparison [32]byte
	copy(comparison[:], stored)
	equal := subtle.ConstantTimeCompare(digest[:], comparison[:])
	if equal != 1 || len(stored) != 32 || !live || !strings.HasPrefix(token, "mgmt_") {
		return c.Principal{}, c.NewError(c.CodeUnauthorized)
	}
	var meta c.ManagementKey
	if json.Unmarshal(b, &meta) != nil {
		return c.Principal{}, persistence()
	}
	return c.Principal{KeyID: meta.ID, Scopes: meta.Scopes}, nil
}
func (s *store) ListKeys(ctx context.Context) (c.KeyList, error) {
	result := c.KeyList{Items: []c.ManagementKey{}}
	rows, err := s.db.QueryContext(ctx, "SELECT metadata FROM management_keys ORDER BY created_at DESC,key_id DESC")
	if err != nil {
		return result, persistence()
	}
	defer rows.Close()
	for rows.Next() {
		key, err := decodeJSON[c.ManagementKey](rows)
		if err != nil {
			return result, persistence()
		}
		result.Items = append(result.Items, key)
	}
	if rows.Err() != nil {
		return result, persistence()
	}
	return result, nil
}
func (s *store) RevokeKey(ctx context.Context, id string) (c.KeyRevoked, error) {
	result := c.KeyRevoked{ID: id, Revoked: true}
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		var b []byte
		if err := tx.QueryRowContext(ctx, "SELECT metadata FROM management_keys WHERE key_id=?", id).Scan(&b); errors.Is(err, sql.ErrNoRows) {
			return c.NewError(c.CodeNotFound)
		} else if err != nil {
			return persistence()
		}
		var key c.ManagementKey
		if json.Unmarshal(b, &key) != nil {
			return persistence()
		}
		if key.RevokedAt == nil {
			now := s.now()
			key.RevokedAt = &now
			b, err := json.Marshal(key)
			if err != nil {
				return persistence()
			}
			if _, err = tx.ExecContext(ctx, "UPDATE management_keys SET revoked_at=?,metadata=? WHERE key_id=?", now.UnixMilli(), b, id); err != nil {
				return persistence()
			}
		}
		return s.cancelForKey(ctx, tx, id)
	})
	return result, err
}
