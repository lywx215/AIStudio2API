// Package files owns Business upload bytes and metadata. It never accesses an
// AI Studio store or an upstream account. Snapshots belong to a single request;
// upstream file IDs must be acquired after that request selects its session.
package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const DefaultMaxBytes int64 = 50 << 20
const DefaultFileTTL = 48 * time.Hour
const DefaultUploadTTL = 30 * time.Minute

var (
	ErrNotFound    = errors.New("Business file or upload was not found")
	ErrOwner       = errors.New("an authenticated Business file owner is required")
	ErrInvalid     = errors.New("invalid Business upload request")
	ErrOffset      = errors.New("upload offset does not match bytes received")
	ErrSize        = errors.New("upload size does not match declared content length")
	ErrTooLarge    = errors.New("Business file exceeds the size limit")
	ErrCapacity    = errors.New("Business file storage capacity is exhausted")
	ErrUnavailable = errors.New("Business file storage is unavailable")
)

type Options struct {
	Now                func() time.Time
	MaxBytes           int64
	FileTTL, UploadTTL time.Duration
	MaxStoredBytes     int64
	MaxRecords         int
}

// Record contains public metadata only. Owner identifiers and upload
// capabilities are intentionally absent.
type Record struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	MIMEType    string    `json:"mime_type"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Snapshot struct {
	Record Record
	Data   []byte
}

type Upload struct {
	Received int64
	Status   string
	File     *Record
}

type manifest struct {
	Version       int       `json:"version"`
	KeyHash       string    `json:"key_hash"`
	Owner         string    `json:"owner"`
	State         string    `json:"state"`
	Expected      int64     `json:"expected"`
	Received      int64     `json:"received"`
	UploadExpires time.Time `json:"upload_expires"`
	File          Record    `json:"file"`
}

type Store struct {
	mu      sync.Mutex
	chunkIO chan struct{}
	dir     string
	options Options
	loaded  bool
	entries map[string]*manifest
	byID    map[string]string
}

// New is inert: even reads of an absent store do not create its directory.
func New(dir string) *Store { return NewWithOptions(dir, Options{}) }

func NewWithOptions(dir string, options Options) *Store {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.FileTTL <= 0 {
		options.FileTTL = DefaultFileTTL
	}
	if options.UploadTTL <= 0 {
		options.UploadTTL = DefaultUploadTTL
	}
	if options.MaxStoredBytes <= 0 {
		options.MaxStoredBytes = 1 << 30
	}
	if options.MaxRecords <= 0 {
		options.MaxRecords = 4096
	}
	return &Store{dir: dir, options: options, chunkIO: make(chan struct{}, 1)}
}

func (s *Store) MaxBytes() int64 { return s.options.MaxBytes }

func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	entries := make(map[string]*manifest)
	byID := make(map[string]string)
	dir, err := os.Open(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		s.entries, s.byID, s.loaded = entries, byID, true
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	// Refuse an unexpectedly large directory rather than reading arbitrary
	// amounts of local state into memory. Each record has at most one blob.
	for scanned := 0; ; {
		items, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return ErrUnavailable
		}
		for _, item := range items {
			scanned++
			if scanned > s.options.MaxRecords*3+128 {
				return ErrCapacity
			}
			if !strings.HasSuffix(item.Name(), ".json") {
				continue
			}
			key := strings.TrimSuffix(item.Name(), ".json")
			if !validHash(key) || item.Type()&os.ModeSymlink != 0 {
				return ErrUnavailable
			}
			f, openErr := os.Open(filepath.Join(s.dir, item.Name()))
			if openErr != nil {
				return ErrUnavailable
			}
			raw, readErr := io.ReadAll(io.LimitReader(f, (64<<10)+1))
			closeErr := f.Close()
			var entry manifest
			if readErr != nil || closeErr != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &entry) != nil || !s.validManifest(key, entry) {
				return ErrUnavailable
			}
			if len(entries) >= s.options.MaxRecords {
				return ErrCapacity
			}
			if _, duplicate := byID[entry.File.ID]; duplicate {
				return ErrUnavailable
			}
			entries[key] = &entry
			byID[entry.File.ID] = key
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	s.entries, s.byID, s.loaded = entries, byID, true
	return nil
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func ValidID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "biz-") {
		return false
	}
	for _, c := range id[4:] {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

func (s *Store) validManifest(key string, e manifest) bool {
	if e.Version != 1 || e.KeyHash != key || e.Owner == "" || !ValidID(e.File.ID) || e.Expected < -1 || e.Expected > s.options.MaxBytes || e.Received < 0 || e.Received > s.options.MaxBytes || e.Expected >= 0 && e.Received > e.Expected || e.UploadExpires.IsZero() {
		return false
	}
	if e.State != "active" && e.State != "final" && e.State != "cancelled" && e.State != "deleted" {
		return false
	}
	return e.State != "final" || e.File.Size == e.Received && validHash(e.File.Hash) && !e.File.ExpiresAt.IsZero()
}

func (s *Store) blob(id string) string      { return filepath.Join(s.dir, id+".blob") }
func (s *Store) metadata(key string) string { return filepath.Join(s.dir, key+".json") }

func (s *Store) persist(e *manifest) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return ErrUnavailable
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return ErrUnavailable
	}
	f, err := os.CreateTemp(s.dir, ".manifest-*")
	if err != nil {
		return ErrUnavailable
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrUnavailable
	}
	if os.Rename(name, s.metadata(e.KeyHash)) != nil {
		return ErrUnavailable
	}
	return nil
}

func tokenKey(token string) (string, error) {
	if !strings.HasPrefix(token, "biz-") {
		return "", ErrNotFound
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "biz-"))
	if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != token[4:] {
		return "", ErrNotFound
	}
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:]), nil
}

func (s *Store) capacity(extra int64) bool {
	used := int64(0)
	for _, e := range s.entries {
		if e.State != "active" && e.State != "final" {
			continue
		}
		size := e.Received
		if e.State == "active" && e.Expected > size {
			size = e.Expected
		}
		if size > s.options.MaxStoredBytes-used {
			return false
		}
		used += size
	}
	return extra <= s.options.MaxStoredBytes-used
}

func (s *Store) Begin(ctx context.Context, owner, name, mimeType string, expected int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if owner == "" {
		return "", ErrOwner
	}
	if expected < -1 {
		return "", ErrInvalid
	}
	if expected > s.options.MaxBytes {
		return "", ErrTooLarge
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "file"
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 512 || strings.ContainsAny(name, "\x00\r\n") {
		return "", ErrInvalid
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil || len(mimeType) > 256 || !strings.Contains(mediaType, "/") {
		return "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return "", err
	}
	if len(s.entries) >= s.options.MaxRecords || !s.capacity(max(expected, 0)) {
		return "", ErrCapacity
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrUnavailable
	}
	token := "biz-" + base64.RawURLEncoding.EncodeToString(random[:])
	key, _ := tokenKey(token)
	if _, err := rand.Read(random[:20]); err != nil {
		return "", ErrUnavailable
	}
	id := "biz-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random[:20]))
	now := s.options.Now().UTC()
	e := &manifest{Version: 1, KeyHash: key, Owner: owner, State: "active", Expected: expected, UploadExpires: now.Add(s.options.UploadTTL), File: Record{ID: id, DisplayName: name, MIMEType: mediaType, CreatedAt: now}}
	if err := s.persist(e); err != nil {
		return "", err
	}
	s.entries[key], s.byID[id] = e, key
	return token, nil
}

// session accepts an empty principal only with the complete random upload
// capability. A supplied principal must match the original upload owner.
func (s *Store) session(token, owner string) (*manifest, error) {
	key, err := tokenKey(token)
	if err != nil {
		return nil, err
	}
	e := s.entries[key]
	if e == nil || owner != "" && e.Owner != owner || !s.options.Now().Before(e.UploadExpires) || e.State == "deleted" || e.State == "final" && !s.options.Now().Before(e.File.ExpiresAt) {
		return nil, ErrNotFound
	}
	return e, nil
}

func uploadState(e *manifest) Upload {
	u := Upload{Received: e.Received, Status: e.State}
	if e.State == "final" {
		record := e.File
		u.File = &record
	}
	return u
}

func (s *Store) Query(ctx context.Context, token, owner string) (Upload, error) {
	if err := ctx.Err(); err != nil {
		return Upload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Upload{}, err
	}
	e, err := s.session(token, owner)
	if err != nil {
		return Upload{}, err
	}
	return uploadState(e), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Chunk commits each complete chunk and metadata before acknowledging it. An
// interrupted read leaves the committed offset unchanged. Unacknowledged tail
// bytes after a process crash are truncated before the next write.
func (s *Store) Chunk(ctx context.Context, token, owner string, offset int64, content io.Reader, finalize bool) (Upload, error) {
	if err := ctx.Err(); err != nil {
		return Upload{}, err
	}
	// Bound aggregate chunk buffers, and keep waiting callers cancellable. A
	// slow client does not hold the metadata lock used by snapshots or cleanup.
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case s.chunkIO <- struct{}{}:
		defer func() { <-s.chunkIO }()
	case <-wait.Done():
		if ctx.Err() != nil {
			return Upload{}, ctx.Err()
		}
		return Upload{}, ErrCapacity
	}
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return Upload{}, err
	}
	e, err := s.session(token, owner)
	if err != nil {
		s.mu.Unlock()
		return Upload{}, err
	}
	if e.State == "cancelled" {
		s.mu.Unlock()
		return Upload{}, ErrNotFound
	}
	if offset < 0 || offset > e.Received || e.State == "active" && offset != e.Received {
		s.mu.Unlock()
		return Upload{}, ErrOffset
	}
	limit := s.options.MaxBytes - offset
	if e.Expected >= 0 {
		limit = min(limit, e.Expected-offset)
	}
	s.mu.Unlock()
	if content == nil {
		content = strings.NewReader("")
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, content}, limit+1))
	if err != nil {
		return Upload{}, err
	}
	if err := ctx.Err(); err != nil {
		return Upload{}, err
	}
	if int64(len(data)) > limit {
		if offset+int64(len(data)) > s.options.MaxBytes {
			return Upload{}, ErrTooLarge
		}
		return Upload{}, ErrSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Delete, cancel and expiry can proceed while a client sends its body.
	e, err = s.session(token, owner)
	if err != nil {
		return Upload{}, err
	}
	if e.State == "cancelled" {
		return Upload{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Upload{}, err
	}
	if offset > e.Received || e.State == "active" && offset != e.Received {
		return Upload{}, ErrOffset
	}
	if e.State == "final" {
		// Only an exact retry of already stored bytes can repeat finalization.
		if !finalize || offset+int64(len(data)) != e.Received {
			return Upload{}, ErrOffset
		}
		if len(data) != 0 {
			f, err := os.Open(s.blob(e.File.ID))
			if err != nil {
				return Upload{}, ErrUnavailable
			}
			stored := make([]byte, len(data))
			_, err = f.ReadAt(stored, offset)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				return Upload{}, ErrUnavailable
			}
			if !equalBytes(stored, data) {
				return Upload{}, ErrSize
			}
		}
		return uploadState(e), nil
	}
	next := *e
	next.Received += int64(len(data))
	if finalize && next.Expected >= 0 && next.Received != next.Expected {
		return Upload{}, ErrSize
	}
	if e.Expected < 0 && !s.capacity(int64(len(data))) {
		return Upload{}, ErrCapacity
	}
	f, err := os.OpenFile(s.blob(e.File.ID), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Upload{}, ErrUnavailable
	}
	stat, err := f.Stat()
	if err == nil && stat.Size() < e.Received {
		err = io.ErrUnexpectedEOF
	}
	if err == nil {
		err = f.Truncate(e.Received)
	}
	if err == nil {
		_, err = f.WriteAt(data, e.Received)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil && finalize {
		hasher := sha256.New()
		_, err = f.Seek(0, io.SeekStart)
		if err == nil {
			_, err = io.Copy(hasher, contextReader{ctx, f})
		}
		if err == nil {
			next.State = "final"
			next.File.Size = next.Received
			next.File.Hash = hex.EncodeToString(hasher.Sum(nil))
			next.File.ExpiresAt = s.options.Now().UTC().Add(s.options.FileTTL)
		}
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		if ctx.Err() != nil {
			return Upload{}, ctx.Err()
		}
		return Upload{}, ErrUnavailable
	}
	if err := s.persist(&next); err != nil {
		return Upload{}, err
	}
	s.entries[e.KeyHash] = &next
	return uploadState(&next), nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) Cancel(ctx context.Context, token, owner string) (Upload, error) {
	if err := ctx.Err(); err != nil {
		return Upload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Upload{}, err
	}
	e, err := s.session(token, owner)
	if err != nil {
		return Upload{}, err
	}
	if e.State == "final" {
		return Upload{}, ErrInvalid
	}
	if e.State != "cancelled" {
		next := *e
		next.State = "cancelled"
		if err := s.persist(&next); err != nil {
			return Upload{}, err
		}
		s.entries[e.KeyHash] = &next
		e = &next
	}
	if err := os.Remove(s.blob(e.File.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Upload{}, ErrUnavailable
	}
	return uploadState(e), nil
}

func (s *Store) record(owner, id string) (*manifest, error) {
	if owner == "" {
		return nil, ErrOwner
	}
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	e := s.entries[s.byID[id]]
	if e == nil || e.Owner != owner || e.State != "final" || !s.options.Now().Before(e.File.ExpiresAt) {
		return nil, ErrNotFound
	}
	return e, nil
}

func (s *Store) Get(ctx context.Context, owner, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Record{}, err
	}
	e, err := s.record(owner, id)
	if err != nil {
		return Record{}, err
	}
	return e.File, nil
}

func (s *Store) Snapshot(ctx context.Context, owner, id string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Snapshot{}, err
	}
	e, err := s.record(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	f, err := os.Open(s.blob(id))
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, e.File.Size+1))
	closeErr := f.Close()
	if err != nil {
		return Snapshot{}, err
	}
	if closeErr != nil {
		return Snapshot{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != e.File.Size || hex.EncodeToString(hash[:]) != e.File.Hash {
		return Snapshot{}, ErrUnavailable
	}
	return Snapshot{Record: e.File, Data: data}, nil
}

func (s *Store) List(ctx context.Context, owner string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, ErrOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	result := make([]Record, 0)
	for _, e := range s.entries {
		if e.Owner == owner && e.State == "final" && s.options.Now().Before(e.File.ExpiresAt) {
			result = append(result, e.File)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// Delete only deletes this broker's retained bytes. No upstream deletion is
// implied: request session attachments have their own upstream lifecycle.
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	e, err := s.record(owner, id)
	if err != nil {
		return err
	}
	next := *e
	next.State = "deleted"
	if err := s.persist(&next); err != nil {
		return err
	}
	s.entries[e.KeyHash] = &next
	return s.remove(&next)
}

func (s *Store) remove(e *manifest) error {
	if err := os.Remove(s.blob(e.File.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.Remove(s.metadata(e.KeyHash)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	delete(s.entries, e.KeyHash)
	delete(s.byID, e.File.ID)
	return nil
}

// Cleanup performs at most limit removals. The host may call it from its own
// bounded lifecycle; the store does not start background goroutines or timers.
func (s *Store) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return 0, err
	}
	removed := 0
	now := s.options.Now()
	for _, e := range s.entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		expired := e.State == "deleted" || e.State == "final" && !now.Before(e.File.ExpiresAt) || e.State != "final" && !now.Before(e.UploadExpires)
		if expired {
			if err := s.remove(e); err != nil {
				return removed, err
			}
			removed++
			if removed >= limit {
				break
			}
		}
	}
	return removed, nil
}
