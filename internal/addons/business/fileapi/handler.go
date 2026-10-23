// Package fileapi implements the native Gemini Files wire protocol for the
// Business-only broker. The host retains authentication and route selection.
package fileapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/files"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

type Handler struct {
	store     *files.Store
	downloads chan struct{}
}

func New(store *files.Store) *Handler {
	return &Handler{store: store, downloads: make(chan struct{}, 2)}
}
func (h *Handler) Public() http.Handler        { return h }
func (h *Handler) UploadHandler() http.Handler { return http.HandlerFunc(h.upload) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if contract.Principal(r.Context()) == "" {
		writeError(w, files.ErrOwner)
		return
	}
	if h.store == nil {
		writeError(w, files.ErrUnavailable)
		return
	}
	// Small opportunistic batches also work when the host has no periodic job.
	if _, err := h.store.Cleanup(r.Context(), 8); err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Path == "/upload/v1beta/files" {
		if r.URL.Query().Has("upload_id") {
			h.upload(w, r)
			return
		}
		if r.Method != http.MethodPost {
			methodError(w, "POST")
			return
		}
		if r.Header.Get(contract.ProviderHeader) != "business" {
			writeError(w, files.ErrInvalid)
			return
		}
		h.start(w, r)
		return
	}
	if r.URL.Path == "/v1beta/files" {
		if r.Method != http.MethodGet {
			methodError(w, "GET")
			return
		}
		if r.Header.Get(contract.ProviderHeader) != "business" {
			writeError(w, files.ErrInvalid)
			return
		}
		h.list(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1beta/files/")
	id, download := strings.CutSuffix(id, ":download")
	if !strings.HasPrefix(r.URL.Path, "/v1beta/files/") || !files.ValidID(id) {
		writeError(w, files.ErrNotFound)
		return
	}
	owner := contract.Principal(r.Context())
	switch r.Method {
	case http.MethodGet:
		if download {
			// Reserve a shared slot before making a full request-owned snapshot.
			// Slow consumers therefore retain at most two download buffers.
			select {
			case h.downloads <- struct{}{}:
				defer func() { <-h.downloads }()
			default:
				writeError(w, files.ErrCapacity)
				return
			}
			r, finishWrite := boundDownloadWrite(w, r)
			defer finishWrite()
			snapshot, err := h.store.Snapshot(r.Context(), owner, id)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Content-Type", snapshot.Record.MIMEType)
			w.Header().Set("Content-Length", strconv.FormatInt(snapshot.Record.Size, 10))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = w.Write(snapshot.Data)
			return
		}
		record, err := h.store.Get(r.Context(), owner, id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, fileObject(record, origin(r)))
	case http.MethodDelete:
		if download {
			writeError(w, files.ErrNotFound)
			return
		}
		if err := h.store.Delete(r.Context(), owner, id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]any{})
	default:
		methodError(w, "GET, DELETE")
	}
}

type metadata struct {
	File struct {
		DisplayName string
		MIMEType    string
		Size        *int64
	}
}

func readMetadata(ctx context.Context, reader io.Reader) (metadata, error) {
	var result metadata
	// The wire metadata is small, but unknown JSON fields can have substantial
	// decoder/map overhead. Admit that bounded work before reading any body.
	release, err := resources.Reserve(ctx, 4<<20)
	if err != nil {
		return result, err
	}
	defer release()
	raw, err := resources.ReadAll(ctx, reader, 64<<10)
	if err != nil {
		return result, err
	}
	if len(raw) > 64<<10 {
		return result, files.ErrInvalid
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return result, nil
	}
	var envelope struct {
		File map[string]json.RawMessage `json:"file"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return result, files.ErrInvalid
	}
	result.File.DisplayName, err = stringAliases(envelope.File, "displayName", "display_name")
	if err != nil {
		return result, err
	}
	result.File.MIMEType, err = stringAliases(envelope.File, "mimeType", "mime_type")
	if err != nil {
		return result, err
	}
	for _, key := range []string{"sizeBytes", "size_bytes"} {
		if value, exists := envelope.File[key]; exists {
			var number string
			if len(value) != 0 && value[0] == '"' {
				if json.Unmarshal(value, &number) != nil {
					return result, files.ErrInvalid
				}
			} else {
				number = string(value)
			}
			parsed, parseErr := strconv.ParseInt(number, 10, 64)
			if parseErr != nil || parsed < 0 {
				return result, files.ErrInvalid
			}
			if result.File.Size != nil && *result.File.Size != parsed {
				return result, files.ErrInvalid
			}
			result.File.Size = &parsed
		}
	}
	return result, nil
}

func stringAliases(fields map[string]json.RawMessage, names ...string) (string, error) {
	var result string
	found := false
	for _, key := range names {
		if value, exists := fields[key]; exists {
			var parsed string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &parsed) != nil {
				return "", files.ErrInvalid
			}
			if found && result != parsed {
				return "", files.ErrInvalid
			}
			result, found = parsed, true
		}
	}
	return result, nil
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	r, finishRead := boundUploadRead(w, r)
	defer finishRead()
	protocol := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Goog-Upload-Protocol")))
	if protocol == "" {
		protocol = r.URL.Query().Get("uploadType")
	}
	if protocol == "multipart" {
		h.multipart(w, r)
		return
	}
	if protocol != "resumable" || strings.ToLower(strings.TrimSpace(r.Header.Get("X-Goog-Upload-Command"))) != "start" {
		writeError(w, files.ErrInvalid)
		return
	}
	declared := r.Header.Get("X-Goog-Upload-Header-Content-Length")
	size, err := strconv.ParseInt(declared, 10, 64)
	if err != nil || size < 0 {
		writeError(w, files.ErrSize)
		return
	}
	meta, err := readMetadata(r.Context(), r.Body)
	if err != nil {
		writeError(w, err)
		return
	}
	if meta.File.Size != nil && *meta.File.Size != size {
		writeError(w, files.ErrSize)
		return
	}
	name := first(meta.File.DisplayName, r.Header.Get("X-Goog-Upload-File-Name"), "file")
	mimeType := first(r.Header.Get("X-Goog-Upload-Header-Content-Type"), meta.File.MIMEType, "application/octet-stream")
	token, err := h.store.Begin(r.Context(), contract.Principal(r.Context()), name, mimeType, size)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("X-Goog-Upload-URL", origin(r)+"/upload/v1beta/files?upload_id="+url.QueryEscape(token)+"&upload_protocol=resumable")
	w.Header().Set("X-Goog-Upload-Status", "active")
	w.Header().Set("X-Goog-Upload-Chunk-Granularity", "1")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) multipart(w http.ResponseWriter, r *http.Request) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		writeError(w, files.ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.store.MaxBytes()+(128<<10))
	reader := multipart.NewReader(r.Body, params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		writeError(w, uploadReadError(r, files.ErrInvalid))
		return
	}
	meta := metadata{}
	partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if partType == "application/json" {
		meta, err = readMetadata(r.Context(), part)
		_ = part.Close()
		if err != nil {
			writeError(w, err)
			return
		}
		part, err = reader.NextPart()
		if err != nil {
			writeError(w, uploadReadError(r, files.ErrInvalid))
			return
		}
	}
	name := first(meta.File.DisplayName, part.FileName(), r.Header.Get("X-Goog-Upload-File-Name"), "file")
	mimeType := first(part.Header.Get("Content-Type"), meta.File.MIMEType, "application/octet-stream")
	owner := contract.Principal(r.Context())
	expected := int64(-1)
	if meta.File.Size != nil {
		expected = *meta.File.Size
	}
	token, err := h.store.Begin(r.Context(), owner, name, mimeType, expected)
	if err != nil {
		writeError(w, err)
		return
	}
	state, err := h.store.Chunk(r.Context(), token, owner, 0, part, true)
	_ = part.Close()
	if err == nil {
		// An extra content part is not silently ignored.
		extra, tailErr := reader.NextPart()
		if extra != nil {
			_ = extra.Close()
		}
		if !errors.Is(tailErr, io.EOF) {
			err = uploadReadError(r, files.ErrInvalid)
		}
	}
	if err != nil {
		if state.File != nil {
			_ = h.store.Delete(context.WithoutCancel(r.Context()), owner, state.File.ID)
		} else {
			_, _ = h.store.Cancel(context.WithoutCancel(r.Context()), token, owner)
		}
		writeError(w, err)
		return
	}
	w.Header().Set("X-Goog-Upload-Status", "final")
	writeJSON(w, map[string]any{"file": fileObject(*state.File, origin(r))})
}

func parseCommands(raw string) (upload, finalize, query, cancel bool, err error) {
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if seen[value] {
			err = files.ErrInvalid
			return
		}
		seen[value] = true
		switch value {
		case "upload":
			upload = true
		case "finalize":
			finalize = true
		case "query":
			query = true
		case "cancel":
			cancel = true
		default:
			err = files.ErrInvalid
			return
		}
	}
	if query && len(seen) != 1 || cancel && len(seen) != 1 {
		err = files.ErrInvalid
	}
	return
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.store == nil {
		writeError(w, files.ErrUnavailable)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		methodError(w, "POST, PUT")
		return
	}
	if r.URL.Path != "/upload/v1beta/files" || len(r.URL.Query()["upload_id"]) != 1 {
		writeError(w, files.ErrNotFound)
		return
	}
	token, owner := r.URL.Query().Get("upload_id"), contract.Principal(r.Context())
	// Validate capability before parsing commands or consuming a request body.
	state, err := h.store.Query(r.Context(), token, owner)
	if err != nil {
		w.Header().Set("X-Goog-Upload-Status", "final")
		writeError(w, err)
		return
	}
	upload, finalize, query, cancel, err := parseCommands(r.Header.Get("X-Goog-Upload-Command"))
	if err != nil {
		uploadHeaders(w, state)
		writeError(w, err)
		return
	}
	switch {
	case cancel:
		state, err = h.store.Cancel(r.Context(), token, owner)
	case query:
	case upload || finalize:
		offset, offsetErr := strconv.ParseInt(r.Header.Get("X-Goog-Upload-Offset"), 10, 64)
		if offsetErr != nil || offset < 0 {
			uploadHeaders(w, state)
			writeError(w, files.ErrOffset)
			return
		}
		var finishRead func()
		r, finishRead = boundUploadRead(w, r)
		defer finishRead()
		var content io.Reader = r.Body
		if !upload {
			// Finalize-only commands cannot discard bytes from a malformed request.
			probe, readErr := io.ReadAll(io.LimitReader(r.Body, 1))
			if readErr != nil || len(probe) != 0 {
				uploadHeaders(w, state)
				writeError(w, uploadReadError(r, files.ErrInvalid))
				return
			}
			content = http.NoBody
		}
		state, err = h.store.Chunk(r.Context(), token, owner, offset, content, finalize)
	}
	if err != nil {
		if current, queryErr := h.store.Query(r.Context(), token, owner); queryErr == nil {
			uploadHeaders(w, current)
		}
		writeError(w, err)
		return
	}
	uploadHeaders(w, state)
	if state.File != nil && finalize {
		writeJSON(w, map[string]any{"file": fileObject(*state.File, origin(r))})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func uploadHeaders(w http.ResponseWriter, state files.Upload) {
	w.Header().Set("X-Goog-Upload-Status", state.Status)
	w.Header().Set("X-Goog-Upload-Size-Received", strconv.FormatInt(state.Received, 10))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	size, offset := 10, 0
	if value := first(query.Get("pageSize"), query.Get("page_size")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, files.ErrInvalid)
			return
		}
		if parsed > 0 {
			size = min(parsed, 100)
		}
	}
	if value := first(query.Get("pageToken"), query.Get("page_token")); value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			writeError(w, files.ErrInvalid)
			return
		}
		parsed, err := strconv.Atoi(string(decoded))
		if err != nil || parsed < 0 {
			writeError(w, files.ErrInvalid)
			return
		}
		offset = parsed
	}
	records, err := h.store.List(r.Context(), contract.Principal(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	end := min(offset, len(records))
	if size <= len(records)-end {
		end += size
	} else {
		end = len(records)
	}
	objects := make([]map[string]any, 0, end-min(offset, len(records)))
	for _, record := range records[min(offset, len(records)):end] {
		objects = append(objects, fileObject(record, origin(r)))
	}
	body := map[string]any{"files": objects}
	if end < len(records) {
		body["nextPageToken"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	writeJSON(w, body)
}

func fileObject(record files.Record, base string) map[string]any {
	hash, _ := hex.DecodeString(record.Hash)
	return map[string]any{
		"name": "files/" + record.ID, "displayName": record.DisplayName, "mimeType": record.MIMEType,
		"sizeBytes": strconv.FormatInt(record.Size, 10), "sha256Hash": base64.StdEncoding.EncodeToString(hash),
		"createTime": record.CreatedAt.UTC().Format(time.RFC3339Nano), "updateTime": record.CreatedAt.UTC().Format(time.RFC3339Nano),
		"expirationTime": record.ExpiresAt.UTC().Format(time.RFC3339Nano), "uri": base + "/v1beta/files/" + record.ID,
		"state": "ACTIVE", "source": "UPLOADED",
	}
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

// Resolver validates local resource identity and reads owner-scoped immutable
// bytes. It never follows a URL, performs a download or accepts a legacy URI.
func (h *Handler) Resolver(r *http.Request) contract.FileResolver {
	base := origin(r)
	return func(ctx context.Context, uri, mimeType string) (contract.Attachment, error) {
		if h.store == nil {
			return contract.Attachment{}, failure(files.ErrUnavailable)
		}
		id, err := resourceID(base, uri)
		if err != nil {
			return contract.Attachment{}, failure(err)
		}
		metadata, err := h.store.Get(ctx, contract.Principal(ctx), id)
		if err != nil {
			return contract.Attachment{}, failure(err)
		}
		if allowance, ok := resources.AttachmentAllowance(ctx); ok && metadata.Size > allowance {
			return contract.Attachment{}, &contract.Failure{Code: 413, Status: "RESOURCE_EXHAUSTED", Message: "Business attachments exceed 50 MiB"}
		}
		if mimeType != "" {
			mediaType, _, parseErr := mime.ParseMediaType(mimeType)
			if parseErr != nil || mediaType != metadata.MIMEType {
				return contract.Attachment{}, failure(files.ErrInvalid)
			}
		}
		snapshot, err := h.store.Snapshot(ctx, contract.Principal(ctx), id)
		if err != nil {
			return contract.Attachment{}, failure(err)
		}
		if mimeType != "" {
			mediaType, _, parseErr := mime.ParseMediaType(mimeType)
			if parseErr != nil || mediaType != snapshot.Record.MIMEType {
				return contract.Attachment{}, failure(files.ErrInvalid)
			}
		}
		return contract.Attachment{Filename: attachmentFilename(snapshot.Record.DisplayName, snapshot.Record.MIMEType, snapshot.Record.Hash), MIMEType: snapshot.Record.MIMEType, Hash: snapshot.Record.Hash, Data: snapshot.Data}, nil
	}
}

// Display names are native Gemini metadata, not local paths or upstream file
// names. Preserve them in Record while deriving a safe session upload name.
func attachmentFilename(display, mimeType, hash string) string {
	name := path.Base(strings.ReplaceAll(display, "\\", "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	tag := ""
	if len(hash) >= 12 {
		tag = "-" + hash[:12]
	}
	if name == "" || name == "." || name == ".." || name == "/" {
		ext := ".bin"
		switch mimeType {
		case "application/pdf":
			ext = ".pdf"
		case "application/json":
			ext = ".json"
		case "application/xml":
			ext = ".xml"
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "image/webp":
			ext = ".webp"
		case "image/heic":
			ext = ".heic"
		case "image/heif":
			ext = ".heif"
		default:
			if strings.HasPrefix(mimeType, "text/") {
				ext = ".txt"
			}
		}
		return "attachment" + tag + ext
	}
	if len(name) > 255 {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		limit := 255 - len(tag) - len(ext)
		stem = stem[:limit]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = strings.TrimSpace(stem) + tag + ext
	}
	return name
}

const uploadReadTimeout = 2 * time.Minute
const downloadWriteTimeout = 2 * time.Minute

type uploadBody struct {
	ctx context.Context
	io.ReadCloser
}

func (body uploadBody) Read(p []byte) (int, error) {
	if err := body.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := body.ReadCloser.Read(p)
	if ctxErr := body.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	// The network deadline can fire just before the context timer is scheduled.
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return n, context.DeadlineExceeded
	}
	return n, err
}

// Read deadlines unblock a stalled network read when the context is cancelled;
// a context timer alone cannot interrupt an already-blocked Request.Body.Read.
func boundUploadRead(w http.ResponseWriter, r *http.Request) (*http.Request, func()) {
	ctx, cancel := context.WithTimeout(r.Context(), uploadReadTimeout)
	control := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = control.SetReadDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = control.SetReadDeadline(time.Now()) })
	r = r.WithContext(ctx)
	r.Body = uploadBody{ctx: ctx, ReadCloser: r.Body}
	return r, func() {
		// Synchronize an already-started callback before resetting a persistent
		// connection's deadline, so it cannot affect the following request.
		if !stop() {
			<-interrupted
		}
		cancel()
		_ = control.SetReadDeadline(time.Time{})
	}
}

func uploadReadError(r *http.Request, err error) error {
	if ctxErr := r.Context().Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func boundDownloadWrite(w http.ResponseWriter, r *http.Request) (*http.Request, func()) {
	ctx, cancel := context.WithTimeout(r.Context(), downloadWriteTimeout)
	control := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = control.SetWriteDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = control.SetWriteDeadline(time.Now()) })
	return r.WithContext(ctx), func() {
		if !stop() {
			<-interrupted
		}
		cancel()
		_ = control.SetWriteDeadline(time.Time{})
	}
}

func resourceID(base, raw string) (string, error) {
	value := raw
	if strings.HasPrefix(value, "files/") {
		value = strings.TrimPrefix(value, "files/")
	} else {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return "", files.ErrInvalid
		}
		if parsed.IsAbs() {
			if parsed.Scheme != "http" && parsed.Scheme != "https" || !strings.EqualFold(parsed.Scheme+"://"+parsed.Host, base) {
				return "", files.ErrInvalid
			}
		} else if parsed.Host != "" {
			return "", files.ErrInvalid
		}
		if !strings.HasPrefix(parsed.Path, "/v1beta/files/") {
			return "", files.ErrInvalid
		}
		value = strings.TrimPrefix(parsed.Path, "/v1beta/files/")
	}
	if !files.ValidID(value) {
		return "", files.ErrInvalid
	}
	return value, nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}
func methodError(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	contract.Error(w, 405, "INVALID_ARGUMENT", "method is not supported for this Business file operation")
}

func failure(err error) *contract.Failure {
	var explicit *contract.Failure
	if errors.As(err, &explicit) {
		return explicit
	}
	code, status, message := http.StatusInternalServerError, "INTERNAL", "Business file storage is unavailable"
	switch {
	case errors.Is(err, files.ErrOwner):
		code, status, message = 401, "UNAUTHENTICATED", err.Error()
	case errors.Is(err, files.ErrNotFound):
		code, status, message = 404, "NOT_FOUND", err.Error()
	case errors.Is(err, files.ErrInvalid), errors.Is(err, files.ErrOffset), errors.Is(err, files.ErrSize):
		code, status, message = 400, "INVALID_ARGUMENT", err.Error()
	case errors.Is(err, files.ErrTooLarge):
		code, status, message = 413, "INVALID_ARGUMENT", err.Error()
	case errors.Is(err, files.ErrCapacity):
		code, status, message = 429, "RESOURCE_EXHAUSTED", err.Error()
	case errors.Is(err, context.Canceled):
		code, status, message = 499, "CANCELLED", "Business file operation was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code, status, message = 504, "DEADLINE_EXCEEDED", "Business file operation exceeded its deadline"
	}
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		code, status, message = 413, "INVALID_ARGUMENT", files.ErrTooLarge.Error()
	}
	return &contract.Failure{Code: code, Status: status, Message: message}
}

func writeError(w http.ResponseWriter, err error) {
	result := failure(err)
	contract.Error(w, result.Code, result.Status, result.Message)
}
