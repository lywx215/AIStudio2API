// Package vault encrypts Business account snapshots independently of AI Studio.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

type Credential struct {
	SecureCSES string `json:"secure_c_ses"`
	HostCOSES  string `json:"host_c_oses"`
	CSESIndex  string `json:"csesidx"`
	ConfigID   string `json:"config_id"`
	UserAgent  string `json:"user_agent"`
}

func (c Credential) Validate() error {
	if c.SecureCSES == "" || c.CSESIndex == "" || c.ConfigID == "" {
		return errors.New("required Business credential fields are missing")
	}
	for _, cookie := range []string{c.SecureCSES, c.HostCOSES} {
		for _, char := range cookie {
			if char < 0x21 || char > 0x7e {
				return errors.New("invalid Business cookie")
			}
		}
		if len(cookie) > 16384 || strings.ContainsAny(cookie, ";\r\n\x00\"\\ ") {
			return errors.New("invalid Business cookie")
		}
	}
	for _, value := range []string{c.CSESIndex, c.ConfigID, c.UserAgent} {
		if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid Business credential metadata")
		}
	}
	return nil
}

type Account struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Enabled    bool       `json:"enabled"`
	Epoch      uint64     `json:"epoch"`
	Credential Credential `json:"credential"`
}
type Snapshot struct {
	Revision uint64    `json:"revision"`
	Accounts []Account `json:"accounts"`
}
type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) *Store { return &Store{dir: dir} }

var ErrUnavailable = errors.New("BUSINESS_VAULT_UNAVAILABLE")
var ErrConflict = errors.New("BUSINESS_ACCOUNT_REVISION_CONFLICT")
var accountID = regexp.MustCompile(`^acct-[a-f0-9]{32}$`)

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "acct-" + hex.EncodeToString(b[:])
}

func (s *Store) Load() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrUnavailable
	}
	return data, nil
}
func (s *Store) load() (Snapshot, error) {
	data, err := readBounded(filepath.Join(s.dir, "accounts.enc"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Accounts: []Account{}}, nil
	}
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	key, err := s.key(false)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	gcm, _ := cipher.NewGCM(block)
	if len(data) < gcm.NonceSize() {
		return Snapshot{}, ErrUnavailable
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("business-accounts-v1"))
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer clear(plain)
	var result Snapshot
	if json.Unmarshal(plain, &result) != nil || len(result.Accounts) > 128 {
		return Snapshot{}, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, a := range result.Accounts {
		if !accountID.MatchString(a.ID) || seen[a.ID] || a.Credential.Validate() != nil {
			return Snapshot{}, ErrUnavailable
		}
		seen[a.ID] = true
	}
	return result, nil
}
func (s *Store) key(create bool) ([]byte, error) {
	path := filepath.Join(s.dir, "key.bin")
	wrapped, err := readBounded(path, 16384)
	if err == nil {
		return unwrapKey(wrapped)
	}
	if !create || !errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnavailable
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	wrapped, err = wrapKey(key)
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		clear(key)
		return s.key(false)
	}
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	_, err = f.Write(wrapped)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	return key, nil
}

// Update serializes a read/modify/atomic replace and rejects stale clients.
func (s *Store) Update(revision uint64, change func(*Snapshot) error) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Snapshot{}, ErrUnavailable
	}
	lock := flock.New(filepath.Join(s.dir, ".write.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = lock.Close()
		return Snapshot{}, ErrConflict
	}
	defer lock.Close()
	current, err := s.load()
	if err != nil {
		return Snapshot{}, err
	}
	if current.Revision != revision {
		return Snapshot{}, ErrConflict
	}
	if err = change(&current); err != nil {
		return Snapshot{}, err
	}
	if len(current.Accounts) > 128 {
		return Snapshot{}, errors.New("account limit reached")
	}
	seen := map[string]bool{}
	for _, a := range current.Accounts {
		if !accountID.MatchString(a.ID) || seen[a.ID] || a.Credential.Validate() != nil {
			return Snapshot{}, errors.New("invalid account snapshot")
		}
		seen[a.ID] = true
	}
	current.Revision++
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return Snapshot{}, ErrUnavailable
	}
	key, err := s.key(true)
	if err != nil {
		return Snapshot{}, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	gcm, _ := cipher.NewGCM(block)
	plain, err := json.Marshal(current)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer clear(plain)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	data := gcm.Seal(nonce, nonce, plain, []byte("business-accounts-v1"))
	f, err := os.CreateTemp(s.dir, ".accounts-*.tmp")
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(s.dir, "accounts.enc"))
	}
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	return current, nil
}
