package fileapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/files"
)

func request(handler http.Handler, method, uri, owner, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, uri, strings.NewReader(body))
	if owner != "" {
		r = r.WithContext(contract.WithPrincipal(r.Context(), owner))
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func startUpload(t *testing.T, h *Handler, size int) string {
	t.Helper()
	w := request(h, "POST", "https://example.test/upload/v1beta/files", "owner", `{"file":{"displayName":"note.txt"}}`, map[string]string{
		contract.ProviderHeader: "business", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": fmt.Sprint(size), "X-Goog-Upload-Header-Content-Type": "text/plain",
	})
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	uri := w.Header().Get("X-Goog-Upload-URL")
	if !strings.HasPrefix(uri, "https://example.test/upload/v1beta/files?upload_id=biz-") || strings.Contains(uri, "key=") {
		t.Fatal("invalid upload URI:", uri)
	}
	return uri
}

func command(h *Handler, uri, owner, cmd, offset, body string) *httptest.ResponseRecorder {
	return request(h.UploadHandler(), "POST", uri, owner, body, map[string]string{"X-Goog-Upload-Command": cmd, "X-Goog-Upload-Offset": offset})
}

func decodedFile(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		File map[string]any `json:"file"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.File
}

func TestNativeResumableProtocolRestartAndIdempotency(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "files")
	h := New(files.New(dir))
	uri := startUpload(t, h, 6)
	w := command(h, uri, "", "upload", "0", "abc")
	if w.Code != 200 || w.Header().Get("X-Goog-Upload-Size-Received") != "3" || w.Header().Get("X-Goog-Upload-Status") != "active" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	h = New(files.New(dir))
	w = command(h, uri, "", "query", "", "")
	if w.Code != 200 || w.Header().Get("X-Goog-Upload-Size-Received") != "3" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	file := decodedFile(t, command(h, uri, "", "upload, finalize", "3", "def"))
	if file["sizeBytes"] != "6" || file["state"] != "ACTIVE" || file["sha256Hash"] == "" || !strings.HasPrefix(file["name"].(string), "files/biz-") || file["expirationTime"] == nil {
		t.Fatal(file)
	}
	retry := decodedFile(t, command(h, uri, "", "upload, finalize", "3", "def"))
	if retry["name"] != file["name"] {
		t.Fatal("finalize minted another file")
	}
	w = command(h, uri, "", "query", "", "")
	if w.Code != 200 || w.Header().Get("X-Goog-Upload-Status") != "final" {
		t.Fatal(w.Code, w.Header())
	}
	fileURI := file["uri"].(string)
	w = request(h, "GET", fileURI, "owner", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "owner") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", fileURI+":download", "owner", "", nil)
	if w.Code != 200 || w.Body.String() != "abcdef" || w.Header().Get("Content-Disposition") != "attachment" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", fileURI, "other", "", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", fileURI+":download", "other", "", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", fileURI, "", "", nil); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "DELETE", fileURI, "other", "", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "DELETE", fileURI, "owner", "", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = command(h, uri, "", "query", "", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNativeUploadRequiresSelectionAndPrincipal(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	headers := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "1"}
	for _, test := range []struct {
		owner, selector string
		want            int
	}{{"owner", "", 400}, {"owner", "other", 400}, {"", "business", 401}} {
		headers[contract.ProviderHeader] = test.selector
		w := request(h, "POST", "https://example.test/upload/v1beta/files", test.owner, "", headers)
		if w.Code != test.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	headers[contract.ProviderHeader] = "business"
	delete(headers, "X-Goog-Upload-Header-Content-Length")
	if w := request(h, "POST", "https://example.test/upload/v1beta/files", "owner", "", headers); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

type unreadableBody struct{ read bool }

func (b *unreadableBody) Read([]byte) (int, error) {
	b.read = true
	return 0, fmt.Errorf("body must not be consumed")
}
func (*unreadableBody) Close() error { return nil }

func TestNativeUploadCapabilityDoesNotTrustPrefixOrForeignOwner(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	uri := startUpload(t, h, 3)
	for _, test := range []struct{ uri, owner string }{{"https://example.test/upload/v1beta/files?upload_id=biz-forged", ""}, {uri, "foreign"}} {
		body := &unreadableBody{}
		r := httptest.NewRequest("POST", test.uri, nil)
		r.Body = body
		if test.owner != "" {
			r = r.WithContext(contract.WithPrincipal(r.Context(), test.owner))
		}
		r.Header.Set("X-Goog-Upload-Command", "upload, finalize")
		r.Header.Set("X-Goog-Upload-Offset", "0")
		w := httptest.NewRecorder()
		h.UploadHandler().ServeHTTP(w, r)
		if w.Code != 404 || body.read {
			t.Fatal(w.Code, body.read, w.Body.String())
		}
	}
	if w := command(h, uri, "", "upload, finalize", "0", "abc"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNativeOffsetsSizeCommandsAndCancel(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	uri := startUpload(t, h, 3)
	for _, test := range []struct{ cmd, offset, body string }{{"upload", "1", "a"}, {"upload", "", "a"}, {"upload", "-1", "a"}, {"upload, finalize", "0", "ab"}, {"upload", "0", "abcd"}, {"query, cancel", "", ""}, {"upload, upload", "0", "a"}, {"unexpected", "0", "a"}, {"finalize", "0", "x"}} {
		w := command(h, uri, "", test.cmd, test.offset, test.body)
		if w.Code != 400 {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
	w := command(h, uri, "", "query", "", "")
	if w.Header().Get("X-Goog-Upload-Size-Received") != "0" {
		t.Fatal("rejected chunks advanced offset")
	}
	w = command(h, uri, "", "cancel", "", "")
	if w.Code != 200 || w.Header().Get("X-Goog-Upload-Status") != "cancelled" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if w = command(h, uri, "", "upload, finalize", "0", "abc"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = command(h, uri, "", "cancel", "", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNativeLimitsAndListPagination(t *testing.T) {
	h := New(files.NewWithOptions(filepath.Join(t.TempDir(), "files"), files.Options{MaxBytes: 3}))
	headers := map[string]string{contract.ProviderHeader: "business", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "4"}
	if w := request(h, "POST", "https://example.test/upload/v1beta/files", "owner", "", headers); w.Code != 413 {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 3 {
		uri := startUpload(t, h, 3)
		decodedFile(t, command(h, uri, "", "upload, finalize", "0", "abc"))
	}
	headers = map[string]string{contract.ProviderHeader: "business"}
	w := request(h, "GET", "https://example.test/v1beta/files?pageSize=2", "owner", "", headers)
	var page struct {
		Files []map[string]any `json:"files"`
		Next  string           `json:"nextPageToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Files) != 2 || page.Next == "" {
		t.Fatal(page, err, w.Body.String())
	}
	w = request(h, "GET", "https://example.test/v1beta/files?pageSize=2&pageToken="+page.Next, "owner", "", headers)
	page = struct {
		Files []map[string]any `json:"files"`
		Next  string           `json:"nextPageToken"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Files) != 1 || page.Next != "" {
		t.Fatal(page, err, w.Body.String())
	}
	w = request(h, "GET", "https://example.test/v1beta/files", "foreign", "", headers)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"files":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", "https://example.test/v1beta/files", "", "", headers); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", "https://example.test/v1beta/files", "owner", "", nil); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(h, "GET", "https://example.test/v1beta/files?pageSize=9223372036854775807&pageToken=OTIyMzM3MjAzNjg1NDc3NTgwNw", "owner", "", headers); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNativeMultipart(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	meta, _ := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json"}})
	_, _ = io.WriteString(meta, `{"file":{"displayName":"multipart.txt"}}`)
	content, _ := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain"}})
	_, _ = io.WriteString(content, "multipart body")
	_ = writer.Close()
	w := request(h, "POST", "https://example.test/upload/v1beta/files?uploadType=multipart", "owner", body.String(), map[string]string{contract.ProviderHeader: "business", "Content-Type": "multipart/related; boundary=" + writer.Boundary()})
	file := decodedFile(t, w)
	if file["displayName"] != "multipart.txt" || file["sizeBytes"] != "14" {
		t.Fatal(file)
	}
}

func TestResolverURIAndOwnerBoundarySnapshot(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	uri := startUpload(t, h, 3)
	file := decodedFile(t, command(h, uri, "", "upload, finalize", "0", "abc"))
	baseRequest := httptest.NewRequest("POST", "https://example.test/v1beta/models/business-auto:generateContent", nil)
	resolver := h.Resolver(baseRequest)
	ctx := contract.WithPrincipal(context.Background(), "owner")
	for _, value := range []string{file["uri"].(string), file["name"].(string), "/v1beta/" + file["name"].(string)} {
		attachment, err := resolver(ctx, value, "text/plain; charset=utf-8")
		if err != nil || string(attachment.Data) != "abc" || attachment.Filename != "note.txt" || attachment.Hash == "" {
			t.Fatal(attachment, err)
		}
	}
	for _, value := range []string{"https://external.test/" + file["name"].(string), "https://example.test.evil/" + file["name"].(string), "http://example.test/v1beta/" + file["name"].(string), file["uri"].(string) + "?key=secret", file["uri"].(string) + "#fragment", "https://user@example.test/v1beta/" + file["name"].(string), "//example.test/v1beta/" + file["name"].(string), "files/legacy", "https://generativelanguage.googleapis.com/v1beta/" + file["name"].(string)} {
		if _, err := resolver(ctx, value, "text/plain"); err == nil {
			t.Fatal("accepted external or invalid URI:", value)
		}
	}
	if _, err := resolver(contract.WithPrincipal(context.Background(), "foreign"), file["uri"].(string), "text/plain"); err == nil {
		t.Fatal("foreign owner accepted")
	}
	if _, err := resolver(context.Background(), file["uri"].(string), "text/plain"); err == nil {
		t.Fatal("missing owner accepted")
	}
	if _, err := resolver(ctx, file["uri"].(string), "image/png"); err == nil {
		t.Fatal("MIME mismatch accepted")
	}
	attachment, _ := resolver(ctx, file["uri"].(string), "text/plain")
	request(h, "DELETE", file["uri"].(string), "owner", "", nil)
	if string(attachment.Data) != "abc" {
		t.Fatal("delete invalidated request snapshot")
	}
	if _, err := resolver(ctx, file["uri"].(string), "text/plain"); err == nil {
		t.Fatal("deleted file restored")
	}
}

func TestNativeUploadExpirySurvivesRestart(t *testing.T) {
	now := time.Now()
	options := files.Options{Now: func() time.Time { return now }}
	dir := filepath.Join(t.TempDir(), "files")
	h := New(files.NewWithOptions(dir, options))
	uri := startUpload(t, h, 3)
	now = now.Add(files.DefaultUploadTTL)
	h = New(files.NewWithOptions(dir, options))
	if w := command(h, uri, "", "upload, finalize", "0", "abc"); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNativeSDKSnakeMetadataAndAliasConflicts(t *testing.T) {
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	headers := map[string]string{contract.ProviderHeader: "business", "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "17", "X-Goog-Upload-Header-Content-Type": "application/pdf"}
	w := request(h, "POST", "https://example.test/upload/v1beta/files", "owner", `{"file":{"display_name":"fixture.pdf","mime_type":"application/pdf","size_bytes":17}}`, headers)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	file := decodedFile(t, command(h, w.Header().Get("X-Goog-Upload-URL"), "", "upload, finalize", "0", strings.Repeat("x", 17)))
	if file["displayName"] != "fixture.pdf" || file["mimeType"] != "application/pdf" || file["sizeBytes"] != "17" {
		t.Fatal(file)
	}
	for _, body := range []string{
		`{"file":{"display_name":"a","displayName":"b"}}`,
		`{"file":{"mime_type":"text/plain","mimeType":"application/pdf"}}`,
		`{"file":{"size_bytes":17,"sizeBytes":"18"}}`,
		`{"file":{"size_bytes":16}}`,
		`{"file":{"sizeBytes":null}}`,
		`{"file":{"display_name":false}}`,
	} {
		if w := request(h, "POST", "https://example.test/upload/v1beta/files", "owner", body, headers); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	w = request(h, "POST", "https://example.test/upload/v1beta/files", "owner", `{"file":{"display_name":"same","displayName":"same","size_bytes":17,"sizeBytes":"17"}}`, headers)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestResolverSafeFilenamePreservesDisplayName(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, test := range []struct{ display, mime, want string }{
		{"reports/2026.txt", "text/plain", "2026.txt"},
		{`C:\reports\2026.txt`, "text/plain", "2026.txt"},
		{"a\tb\x00c.txt", "text/plain", "abc.txt"},
		{"..", "application/pdf", "attachment-aaaaaaaaaaaa.pdf"},
		{"\t", "image/png", "attachment-aaaaaaaaaaaa.png"},
		{"", "application/json", "attachment-aaaaaaaaaaaa.json"},
	} {
		if got := attachmentFilename(test.display, test.mime, hash); got != test.want {
			t.Fatalf("filename %q: %q, want %q", test.display, got, test.want)
		}
	}
	longName := strings.Repeat("附件", 100) + ".pdf"
	got := attachmentFilename(longName, "application/pdf", hash)
	if len(got) > 255 || !utf8.ValidString(got) || !strings.HasSuffix(got, "-aaaaaaaaaaaa.pdf") || strings.ContainsAny(got, "/\\") || strings.IndexFunc(got, unicode.IsControl) >= 0 {
		t.Fatalf("unsafe truncated filename: %q", got)
	}
	store := files.New(filepath.Join(t.TempDir(), "files"))
	h := New(store)
	token, err := store.Begin(context.Background(), "owner", "reports/2026.txt", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Chunk(context.Background(), token, "owner", 0, strings.NewReader("abc"), true)
	if err != nil {
		t.Fatal(err)
	}
	base := httptest.NewRequest("POST", "https://example.test/", nil)
	attachment, err := h.Resolver(base)(contract.WithPrincipal(context.Background(), "owner"), "files/"+state.File.ID, "text/plain")
	if err != nil || attachment.Filename != "2026.txt" {
		t.Fatal(attachment.Filename, err)
	}
	record, err := store.Get(context.Background(), "owner", state.File.ID)
	if err != nil || record.DisplayName != "reports/2026.txt" {
		t.Fatal(record, err)
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines chan time.Time
	release   chan struct{}
	once      sync.Once
}

func (w *deadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadlines <- deadline
	if !deadline.IsZero() && !deadline.After(time.Now()) && w.release != nil {
		w.once.Do(func() { close(w.release) })
	}
	return nil
}

type stalledUploadBody struct {
	started chan struct{}
	release chan struct{}
}

func (b stalledUploadBody) Read([]byte) (int, error) { close(b.started); <-b.release; return 0, io.EOF }
func (b stalledUploadBody) Close() error             { return nil }

func TestUploadCancellationInterruptsReadAndResetsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), deadlines: make(chan time.Time, 4), release: release}
	r := httptest.NewRequest("POST", "https://example.test/upload/v1beta/files", nil)
	r = r.WithContext(contract.WithPrincipal(ctx, "owner"))
	r.Body = stalledUploadBody{started: started, release: release}
	r.Header.Set(contract.ProviderHeader, "business")
	r.Header.Set("X-Goog-Upload-Protocol", "resumable")
	r.Header.Set("X-Goog-Upload-Command", "start")
	r.Header.Set("X-Goog-Upload-Header-Content-Length", "3")
	h := New(files.New(filepath.Join(t.TempDir(), "files")))
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not enter body read")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled upload left its body read blocked")
	}
	if w.Code != 499 {
		t.Fatal(w.Code, w.Body.String())
	}
	initial, interrupted, reset := <-w.deadlines, <-w.deadlines, <-w.deadlines
	if initial.Before(time.Now().Add(time.Minute)) || interrupted.After(time.Now()) || !reset.IsZero() {
		t.Fatal(initial, interrupted, reset)
	}
}

func TestDownloadCapacityRejectsBeforeSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "files")
	store := files.New(dir)
	h := New(store)
	token, err := store.Begin(context.Background(), "owner", "file.txt", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Chunk(context.Background(), token, "owner", 0, strings.NewReader("abc"), true)
	if err != nil {
		t.Fatal(err)
	}
	// A snapshot of this record now fails. A full gate must reject before
	// attempting that read and return its own capacity status instead.
	if err := os.Remove(filepath.Join(dir, state.File.ID+".blob")); err != nil {
		t.Fatal(err)
	}
	h.downloads <- struct{}{}
	h.downloads <- struct{}{}
	uri := "https://example.test/v1beta/files/" + state.File.ID + ":download"
	if w := request(h, "GET", uri, "owner", "", nil); w.Code != 429 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-h.downloads
	if w := request(h, "GET", uri, "owner", "", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-h.downloads
}

type stalledDownloadWriter struct {
	*httptest.ResponseRecorder
	deadlines chan time.Time
	writing   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (w *stalledDownloadWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines <- deadline
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.release) })
	}
	return nil
}
func (w *stalledDownloadWriter) Write([]byte) (int, error) {
	close(w.writing)
	<-w.release
	return 0, context.Canceled
}

func TestDownloadSlotsAreSharedAndCancellationReleasesBuffers(t *testing.T) {
	store := files.New(filepath.Join(t.TempDir(), "files"))
	h := New(store)
	token, err := store.Begin(context.Background(), "owner", "file.txt", "text/plain", 3)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Chunk(context.Background(), token, "owner", 0, strings.NewReader("abc"), true)
	if err != nil {
		t.Fatal(err)
	}
	uri := "https://example.test/v1beta/files/" + state.File.ID + ":download"
	begin := func() (*stalledDownloadWriter, context.CancelFunc, <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		w := &stalledDownloadWriter{ResponseRecorder: httptest.NewRecorder(), deadlines: make(chan time.Time, 4), writing: make(chan struct{}), release: make(chan struct{})}
		r := httptest.NewRequest("GET", uri, nil).WithContext(contract.WithPrincipal(ctx, "owner"))
		done := make(chan struct{})
		go func() { h.ServeHTTP(w, r); close(done) }()
		select {
		case <-w.writing:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("download did not reach write")
		}
		return w, cancel, done
	}
	one, cancelOne, doneOne := begin()
	defer cancelOne()
	two, cancelTwo, doneTwo := begin()
	defer cancelTwo()
	if w := request(h, "GET", uri, "owner", "", nil); w.Code != 429 {
		t.Fatal(w.Code, w.Body.String())
	}
	cancelOne()
	select {
	case <-doneOne:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled consumer kept its download slot")
	}
	if w := request(h, "GET", uri, "owner", "", nil); w.Code != 200 || w.Body.String() != "abc" {
		t.Fatal(w.Code, w.Body.String())
	}
	cancelTwo()
	select {
	case <-doneTwo:
	case <-time.After(2 * time.Second):
		t.Fatal("second consumer kept its download slot")
	}
	for _, w := range []*stalledDownloadWriter{one, two} {
		initial, interrupted, reset := <-w.deadlines, <-w.deadlines, <-w.deadlines
		if initial.Before(time.Now().Add(time.Minute)) || interrupted.After(time.Now()) || !reset.IsZero() {
			t.Fatal(initial, interrupted, reset)
		}
	}
	if len(h.downloads) != 0 {
		t.Fatal("completed downloads leaked slots")
	}
}
