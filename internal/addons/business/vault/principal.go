package vault

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Principal derives a stable, non-reversible file owner from this vault's
// protected root key. It is called only after the host has authenticated a model
// key and explicitly enabled Business, never during default initialization.
func (s *Store) Principal(modelKey string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return "", ErrUnavailable
	}
	lock := flock.New(filepath.Join(s.dir, ".write.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = lock.Close()
		return "", ErrUnavailable
	}
	defer lock.Close()
	key, err := s.key(true)
	if err != nil {
		return "", ErrUnavailable
	}
	defer clear(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("business-file-principal-v1\x00"))
	_, _ = mac.Write([]byte(modelKey))
	return "biz-owner-" + hex.EncodeToString(mac.Sum(nil)), nil
}
