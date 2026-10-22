package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	return NewWithOptions(filepath.Join(t.TempDir(), "business-files"), Options{Now: func() time.Time { return now }, MaxBytes: 16}), &now
}

func completeFile(t *testing.T, s *Store, owner, content string) (string, Record) {
	t.Helper()
	token, err := s.Begin(context.Background(), owner, "note.txt", "text/plain; charset=utf-8", int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Chunk(context.Background(), token, owner, 0, strings.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	if state.File == nil {
		t.Fatal("missing finalized file")
	}
	return token, *state.File
}

func TestStoreLazyAndOwnerIsolation(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor created a directory")
	}
	if records, err := s.List(context.Background(), "owner-a"); err != nil || len(records) != 0 {
		t.Fatalf("list: %v %v", records, err)
	}
	if _, err := os.Stat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created a directory")
	}
	_, record := completeFile(t, s, "owner-a", "hello")
	if _, err := s.Get(context.Background(), "", record.ID); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "owner-b", record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(context.Background(), "owner-b", record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), "owner-b", record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if records, err := s.List(context.Background(), "owner-b"); err != nil || len(records) != 0 {
		t.Fatalf("owner leaked: %v %v", records, err)
	}
	if records, err := s.List(context.Background(), "owner-a"); err != nil || len(records) != 1 {
		t.Fatalf("list: %v %v", records, err)
	}
}

func TestStoreResumableRestartAndIdempotentFinalize(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	token, err := s.Begin(ctx, "owner", "file", "text/plain", 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("abc"), false); err != nil {
		t.Fatal(err)
	}
	// A capability survives process restart without storing the bearer token.
	restarted := NewWithOptions(s.dir, s.options)
	state, err := restarted.Query(ctx, token, "")
	if err != nil || state.Received != 3 {
		t.Fatalf("restart: %+v %v", state, err)
	}
	if _, err := restarted.Query(ctx, token, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := restarted.Chunk(ctx, token, "", 2, strings.NewReader("def"), true); !errors.Is(err, ErrOffset) {
		t.Fatal(err)
	}
	state, err = restarted.Chunk(ctx, token, "", 3, strings.NewReader("def"), true)
	if err != nil {
		t.Fatal(err)
	}
	id := state.File.ID
	state, err = restarted.Chunk(ctx, token, "", 3, strings.NewReader("def"), true)
	if err != nil || state.File.ID != id {
		t.Fatalf("retry: %+v %v", state, err)
	}
	state, err = restarted.Chunk(ctx, token, "", 6, httpNoBody{}, true)
	if err != nil || state.File.ID != id {
		t.Fatalf("finalize: %+v %v", state, err)
	}
	if _, err := restarted.Chunk(ctx, token, "", 3, strings.NewReader("BAD"), true); !errors.Is(err, ErrSize) {
		t.Fatal(err)
	}
	finalRestart := NewWithOptions(s.dir, s.options)
	snapshot, err := finalRestart.Snapshot(ctx, "owner", id)
	if err != nil || string(snapshot.Data) != "abcdef" {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	for _, e := range finalRestart.entries {
		raw, err := os.ReadFile(finalRestart.metadata(e.KeyHash))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) {
			t.Fatal("raw capability persisted")
		}
	}
}

type httpNoBody struct{}

func (httpNoBody) Read([]byte) (int, error) { return 0, io.EOF }

func TestStoreCapabilityValidationAndTTL(t *testing.T) {
	s, now := newTestStore(t)
	ctx := context.Background()
	token, _ := s.Begin(ctx, "owner", "file", "text/plain", 3)
	for _, forged := range []string{"biz-", token + "x", "biz-" + strings.Repeat("A", 43)} {
		if _, err := s.Query(ctx, forged, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("capability %q: %v", forged, err)
		}
	}
	*now = now.Add(DefaultUploadTTL)
	if _, err := s.Query(ctx, token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("abc"), true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStoreTTLDeleteSnapshotAndBoundedCleanup(t *testing.T) {
	s, now := newTestStore(t)
	ctx := context.Background()
	token, first := completeFile(t, s, "owner", "first")
	_, second := completeFile(t, s, "owner", "second")
	snapshot, err := s.Snapshot(ctx, "owner", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "owner", first.ID); err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Data) != "first" {
		t.Fatal("delete changed an in-flight snapshot")
	}
	if _, err := s.Query(ctx, token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.blob(first.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blob was not deleted")
	}
	_, _ = completeFile(t, s, "owner", "third")
	*now = now.Add(DefaultFileTTL)
	if _, err := s.Get(ctx, "owner", second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if records, err := s.List(ctx, "owner"); err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
	if count, err := s.Cleanup(ctx, 1); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if len(s.entries) != 1 {
		t.Fatal("cleanup exceeded its batch limit")
	}
	if count, err := NewWithOptions(s.dir, s.options).Cleanup(ctx, 1); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

type interruptedReader struct{ read bool }

func (r *interruptedReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("synthetic interrupted body")
	}
	r.read = true
	return copy(p, "ab"), nil
}

func TestStoreInterruptedChunkSizeAndCancel(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	token, err := s.Begin(ctx, "owner", "file", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, &interruptedReader{}, false); err == nil {
		t.Fatal("interruption was swallowed")
	}
	state, err := s.Query(ctx, token, "")
	if err != nil || state.Received != 0 {
		t.Fatal(state, err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("abcd"), true); !errors.Is(err, ErrSize) {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("ab"), true); !errors.Is(err, ErrSize) {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("ab"), false); err != nil {
		t.Fatal(err)
	}
	state, err = s.Cancel(ctx, token, "")
	if err != nil || state.Status != "cancelled" {
		t.Fatal(state, err)
	}
	if _, err := s.Chunk(ctx, token, "", 2, strings.NewReader("c"), true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, token, ""); err != nil {
		t.Fatal("cancel should be idempotent:", err)
	}
	if _, err := s.Begin(ctx, "owner", "file", "text/plain", 17); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestStoreCrashTailIsNotAcknowledged(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	token, err := s.Begin(ctx, "owner", "file", "text/plain", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("ab"), false); err != nil {
		t.Fatal(err)
	}
	key, _ := tokenKey(token)
	id := s.entries[key].File.ID
	f, err := os.OpenFile(s.blob(id), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("unacknowledged")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	restarted := NewWithOptions(s.dir, s.options)
	if _, err := restarted.Chunk(ctx, token, "", 2, strings.NewReader("cd"), true); err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.Snapshot(ctx, "owner", id)
	if err != nil || string(snapshot.Data) != "abcd" {
		t.Fatal(snapshot, err)
	}
}

func TestStoreSnapshotImmutableAndConcurrentFinalize(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	token, err := s.Begin(ctx, "owner", "file", "text/plain", 4)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Chunk(ctx, token, "", 0, strings.NewReader("same"), true)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, _ := s.Query(ctx, token, "")
	first, _ := s.Snapshot(ctx, "owner", state.File.ID)
	first.Data[0] = 'X'
	second, _ := s.Snapshot(ctx, "owner", state.File.ID)
	if string(second.Data) != "same" {
		t.Fatal("request-owned snapshot altered retained file")
	}
}

func TestStoreQuotaAndContextCancellation(t *testing.T) {
	s, _ := newTestStore(t)
	s.options.MaxStoredBytes = 6
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Begin(ctx, "owner", "file", "text/plain", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	token, err := s.Begin(context.Background(), "owner", "file", "text/plain", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(context.Background(), "owner", "file", "text/plain", 2); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := s.Chunk(ctx, token, "", 0, strings.NewReader("hello"), true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Cancel(context.Background(), token, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(context.Background(), "owner", "file", "text/plain", 6); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCorruptionIsNotSilentlyReset(t *testing.T) {
	s, _ := newTestStore(t)
	token, _ := completeFile(t, s, "owner", "data")
	key, _ := tokenKey(token)
	if err := os.WriteFile(s.metadata(key), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.dir).List(context.Background(), "owner"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

type slowChunkReader struct {
	started chan struct{}
	release chan struct{}
	done    bool
}

func (r *slowChunkReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	close(r.started)
	<-r.release
	return copy(p, "abc"), io.EOF
}

func TestStoreSlowUploadAllowsQueryCancelAndCancellableWaiters(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	token, err := s.Begin(ctx, "owner", "file", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	reader := &slowChunkReader{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer release()
	finished := make(chan error, 1)
	go func() { _, err := s.Chunk(ctx, token, "", 0, reader, true); finished <- err }()
	select {
	case <-reader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("chunk did not start")
	}
	queryDone := make(chan error, 1)
	go func() { _, err := s.Query(ctx, token, ""); queryDone <- err }()
	select {
	case err := <-queryDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow request held metadata lock")
	}
	waiterCtx, cancel := context.WithCancel(ctx)
	waiterDone := make(chan error, 1)
	go func() { _, err := s.Chunk(waiterCtx, token, "", 0, strings.NewReader("abc"), true); waiterDone <- err }()
	cancel()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("I/O waiter ignored cancellation")
	}
	if _, err := s.Cancel(ctx, token, ""); err != nil {
		t.Fatal(err)
	}
	// The upload may finish receiving bytes later, but cancellation prevents
	// those bytes from resurrecting a resource or advancing its offset.
	release()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled chunk did not finish")
	}
}

type cancelAtEOFReader struct{ cancel context.CancelFunc }

func (r cancelAtEOFReader) Read(p []byte) (int, error) { r.cancel(); return copy(p, "abc"), io.EOF }

func TestStoreCancellationAtFinalReadDoesNotCommit(t *testing.T) {
	s, _ := newTestStore(t)
	token, err := s.Begin(context.Background(), "owner", "file", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.Chunk(ctx, token, "owner", 0, cancelAtEOFReader{cancel}, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := s.Query(context.Background(), token, "owner")
	if err != nil || state.Received != 0 || state.Status != "active" {
		t.Fatal(state, err)
	}
}
