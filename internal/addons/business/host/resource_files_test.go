package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

const resourceFileOwner = "synthetic-file-budget-owner"

func resourceFileHost(t *testing.T) *Host {
	t.Helper()
	var calls atomic.Int32
	h, _ := hotUpdateHost(t, offlineTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return fixtureResponse(500, "{}"), nil
	}))
	t.Cleanup(func() {
		if calls.Load() != 0 {
			t.Errorf("local file operations contacted upstream %d times", calls.Load())
		}
	})
	return h
}

func resourceFileStart(t *testing.T, h *Host, data string) string {
	t.Helper()
	w := resourceFileStartRequest(h, data)
	if w.Code != http.StatusOK || w.Header().Get("X-Goog-Upload-URL") == "" {
		t.Fatalf("start failed: status=%d body=%s", w.Code, w.Body.String())
	}
	return w.Header().Get("X-Goog-Upload-URL")
}

func resourceFileStartRequest(h *Host, data string) *httptest.ResponseRecorder {
	size := strconv.Itoa(len(data))
	return ownedExchange(h, resourceFileOwner, "POST", "http://localhost/upload/v1beta/files",
		`{"file":{"display_name":"budget.txt","mime_type":"text/plain","size_bytes":"`+size+`"}}`,
		map[string]string{
			contract.ProviderHeader:               "business",
			"X-Goog-Upload-Protocol":              "resumable",
			"X-Goog-Upload-Command":               "start",
			"X-Goog-Upload-Header-Content-Length": size,
			"X-Goog-Upload-Header-Content-Type":   "text/plain",
		}, false)
}

func resourceFileChunkRequest(ctx context.Context, address string, body io.Reader, capabilityRoute bool) *http.Request {
	r := httptest.NewRequest("POST", address, body)
	r = r.WithContext(ctx)
	if !capabilityRoute {
		r = r.WithContext(contract.WithPrincipal(r.Context(), resourceFileOwner))
	}
	r.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	r.Header.Set("X-Goog-Upload-Offset", "0")
	return r
}

func resourceFileHandler(h *Host, capabilityRoute bool) http.Handler {
	if capabilityRoute {
		return h.UploadHandler()
	}
	return h.PublicHandler()
}

func resourceFileFinalize(t *testing.T, h *Host, address, data string, capabilityRoute bool) string {
	t.Helper()
	w := httptest.NewRecorder()
	resourceFileHandler(h, capabilityRoute).ServeHTTP(w, resourceFileChunkRequest(context.Background(), address, strings.NewReader(data), capabilityRoute))
	if w.Code != http.StatusOK {
		t.Fatalf("finalize failed: status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct{ File struct{ Name string } }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !strings.HasPrefix(result.File.Name, "files/biz-") {
		t.Fatalf("invalid file response: %v", err)
	}
	return "http://localhost/v1beta/" + result.File.Name + ":download"
}

func resourceFileBudget(t *testing.T, h *Host, want int64) {
	t.Helper()
	s := h.budget.Snapshot()
	if s.InUseBytes != want || s.AvailableBytes != max(0, s.LimitBytes-want) {
		t.Fatalf("unexpected resource snapshot: %+v, want in-use=%d", s, want)
	}
}

func resourceFileFailure(t *testing.T, w *httptest.ResponseRecorder, code int, status string) {
	t.Helper()
	var result struct {
		Error struct {
			Code   int
			Status string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != code || result.Error.Code != code || result.Error.Status != status {
		t.Fatalf("unexpected file error: status=%d body=%s parse=%v", w.Code, w.Body.String(), err)
	}
}

func resourceFileWait(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not complete", operation)
	}
}

func TestHostFileUploadMetadataSharesResourceBudget(t *testing.T) {
	h := resourceFileHost(t)
	_, held := h.budget.Begin(context.Background())
	defer held.Close()
	limit := h.budget.Snapshot().LimitBytes
	if _, err := held.Reserve(limit); err != nil {
		t.Fatal(err)
	}
	w := resourceFileStartRequest(h, "local fixture")
	resourceFileFailure(t, w, 429, "RESOURCE_EXHAUSTED")
	resourceFileBudget(t, h, limit)
	held.Close()
	resourceFileStart(t, h, "local fixture")
	resourceFileBudget(t, h, 0)
}

func TestHostFileUploadEntrypointsShareResourceBudget(t *testing.T) {
	for _, capabilityRoute := range []bool{false, true} {
		name := "PublicHandler"
		if capabilityRoute {
			name = "UploadHandler"
		}
		t.Run(name, func(t *testing.T) {
			h := resourceFileHost(t)
			data := "local fixture"
			address := resourceFileStart(t, h, data)
			_, held := h.budget.Begin(context.Background())
			defer held.Close()
			limit := h.budget.Snapshot().LimitBytes
			if _, err := held.Reserve(limit); err != nil {
				t.Fatal(err)
			}
			body := &countedBody{}
			w := httptest.NewRecorder()
			resourceFileHandler(h, capabilityRoute).ServeHTTP(w, resourceFileChunkRequest(context.Background(), address, body, capabilityRoute))
			resourceFileFailure(t, w, 429, "RESOURCE_EXHAUSTED")
			if body.reads.Load() != 0 {
				t.Fatal("exhausted file upload consumed the request body")
			}
			resourceFileBudget(t, h, limit)
			held.Close()
			// A rejected chunk leaves the committed offset unchanged and can be retried.
			uri := resourceFileFinalize(t, h, address, data, capabilityRoute)
			resourceFileBudget(t, h, 0)
			download := ownedExchange(h, resourceFileOwner, "GET", uri, "", nil, false)
			if download.Code != 200 || download.Body.String() != data {
				t.Fatalf("download failed after retry: status=%d body=%s", download.Code, download.Body.String())
			}
			resourceFileBudget(t, h, 0)
		})
	}
}

type resourceFileCancelledBody struct {
	ctx     context.Context
	entered chan struct{}
	once    sync.Once
}

func (body *resourceFileCancelledBody) Read([]byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func TestHostFileUploadCancellationReturnsOnlyItsResourceBudget(t *testing.T) {
	for _, capabilityRoute := range []bool{false, true} {
		name := "PublicHandler"
		if capabilityRoute {
			name = "UploadHandler"
		}
		t.Run(name, func(t *testing.T) {
			h := resourceFileHost(t)
			data := "local fixture"
			address := resourceFileStart(t, h, data)
			_, other := h.budget.Begin(context.Background())
			defer other.Close()
			const otherBytes = int64(17)
			if _, err := other.Reserve(otherBytes); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &resourceFileCancelledBody{ctx: ctx, entered: make(chan struct{})}
			w, done := httptest.NewRecorder(), make(chan struct{})
			go func() {
				resourceFileHandler(h, capabilityRoute).ServeHTTP(w, resourceFileChunkRequest(ctx, address, body, capabilityRoute))
				close(done)
			}()
			resourceFileWait(t, body.entered, "upload body read")
			if h.budget.Snapshot().InUseBytes <= otherBytes {
				t.Fatal("upload read buffer was not charged to the shared budget")
			}
			cancel()
			resourceFileWait(t, done, "cancelled upload")
			resourceFileFailure(t, w, 499, "CANCELLED")
			resourceFileBudget(t, h, otherBytes)
			// Cancellation must not acknowledge any body bytes or consume its capability.
			resourceFileFinalize(t, h, address, data, capabilityRoute)
			resourceFileBudget(t, h, otherBytes)
			other.Close()
			resourceFileBudget(t, h, 0)
		})
	}
}

type resourceFileBlockedWriter struct {
	*httptest.ResponseRecorder
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *resourceFileBlockedWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.release) })
	}
	return nil
}

func (w *resourceFileBlockedWriter) Write([]byte) (int, error) {
	close(w.writing)
	<-w.release
	return 0, context.Canceled
}

func TestHostFileDownloadSharesBudgetAndCancellationReleasesSnapshot(t *testing.T) {
	h := resourceFileHost(t)
	data := "local download fixture"
	address := resourceFileStart(t, h, data)
	uri := resourceFileFinalize(t, h, address, data, true)
	otherAddress := resourceFileStart(t, h, data)
	resourceFileBudget(t, h, 0)

	_, held := h.budget.Begin(context.Background())
	defer held.Close()
	limit := h.budget.Snapshot().LimitBytes
	if _, err := held.Reserve(limit); err != nil {
		t.Fatal(err)
	}
	rejected := ownedExchange(h, resourceFileOwner, "GET", uri, "", nil, false)
	resourceFileFailure(t, rejected, 429, "RESOURCE_EXHAUSTED")
	resourceFileBudget(t, h, limit)
	held.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &resourceFileBlockedWriter{ResponseRecorder: httptest.NewRecorder(), writing: make(chan struct{}), release: make(chan struct{})}
	defer w.once.Do(func() { close(w.release) })
	r := httptest.NewRequest("GET", uri, nil).WithContext(contract.WithPrincipal(ctx, resourceFileOwner))
	done := make(chan struct{})
	go func() { h.PublicHandler().ServeHTTP(w, r); close(done) }()
	resourceFileWait(t, w.writing, "download snapshot write")
	snapshot := h.budget.Snapshot()
	if snapshot.InUseBytes < int64(len(data)) {
		t.Fatal("download snapshot was not charged while the response held its bytes")
	}
	_, other := h.budget.Begin(context.Background())
	defer other.Close()
	if _, err := other.Reserve(snapshot.AvailableBytes); err != nil {
		t.Fatal(err)
	}
	// The retained download snapshot and the capability upload share one limit.
	upload := httptest.NewRecorder()
	h.UploadHandler().ServeHTTP(upload, resourceFileChunkRequest(context.Background(), otherAddress, strings.NewReader(data), true))
	resourceFileFailure(t, upload, 429, "RESOURCE_EXHAUSTED")
	resourceFileBudget(t, h, limit)
	cancel()
	resourceFileWait(t, done, "cancelled download")
	resourceFileBudget(t, h, snapshot.AvailableBytes)
	other.Close()
	resourceFileBudget(t, h, 0)

	resourceFileFinalize(t, h, otherAddress, data, true)
	download := ownedExchange(h, resourceFileOwner, "GET", uri, "", nil, false)
	if download.Code != 200 || download.Body.String() != data {
		t.Fatalf("download did not recover: status=%d body=%s", download.Code, download.Body.String())
	}
	resourceFileBudget(t, h, 0)
}
