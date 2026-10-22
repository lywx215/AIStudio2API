// Command token-count measures exact UTF-8 text through the Business file
// ingestion counter. It is opt-in, never submits a generation, and does not
// interpret ingestion tokenCount as Argon generation or billing usage.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

const (
	authHost         = "business.gemini.google"
	apiHost          = "biz-discoveryengine.googleapis.com"
	authPath         = "/auth/getoxsrf"
	apiPrefix        = "/v1alpha/locations/global/"
	maxFileBytes     = 50 << 20
	maxResponseBytes = 1 << 20
)

var stopBeforeGeneration = contract.Fail(499, "CANCELLED", "File counter stopped before generation")

type report struct {
	FileSHA256                   string         `json:"file_sha256"`
	FileBytes                    int            `json:"file_bytes"`
	TokenCount                   *int64         `json:"token_count"`
	CountSource                  string         `json:"count_source"`
	CountingLimitation           string         `json:"counting_limitation"`
	HTTPStatus                   map[string]int `json:"http_status"`
	ResponseKeys                 []string       `json:"response_keys"`
	FileResponseKeys             []string       `json:"add_context_file_response_keys"`
	CounterParseStatus           string         `json:"counter_parse_status"`
	GenerationRequestSent        bool           `json:"generation_request_sent"`
	GenerationRequestIntercepted bool           `json:"generation_request_intercepted"`
	GenerationEvents             int            `json:"generation_events"`
	Completed                    bool           `json:"completed"`
	Failure                      string         `json:"failure,omitempty"`
	CompletedAt                  string         `json:"completed_at"`
}

type counterTransport struct {
	base   http.RoundTripper
	result *report
	sent   map[string]bool
}

func (t *counterTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Inspect the URL before the underlying transport can see Authorization or
	// cookies. No alternate host, port, path, redirect or generation is allowed.
	u := request.URL
	if u == nil || u.Scheme != "https" || u.Port() != "" || u.User != nil || u.Fragment != "" || u.RawPath != "" || request.Host != u.Host {
		return nil, contract.Fail(400, "INVALID_ARGUMENT", "Unexpected counter endpoint")
	}
	if u.Host == apiHost && u.Path == apiPrefix+"widgetStreamAssist" {
		t.result.GenerationRequestIntercepted = true
		return nil, stopBeforeGeneration
	}
	stage := ""
	switch {
	case u.Host == authHost && u.Path == authPath && request.Method == http.MethodGet:
		query := u.Query()
		if len(query) != 1 || len(query["csesidx"]) != 1 || query.Get("csesidx") == "" || request.Header.Get("Authorization") != "" {
			return nil, contract.Fail(400, "INVALID_ARGUMENT", "Unexpected counter authentication request")
		}
		stage = "authentication"
	case u.Host == apiHost && request.Method == http.MethodPost && u.RawQuery == "" && request.Header.Get("Cookie") == "":
		switch u.Path {
		case apiPrefix + "widgetCreateSession":
			stage = "create_session"
		case apiPrefix + "widgetAddContextFile":
			stage = "add_context_file"
		}
	}
	if stage == "" || t.sent[stage] {
		return nil, contract.Fail(400, "INVALID_ARGUMENT", "Unexpected or repeated counter operation")
	}
	t.sent[stage] = true
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, contract.Fail(502, "UNAVAILABLE", "Counter upstream transport failed")
	}
	if response == nil || response.Body == nil {
		return nil, contract.Fail(502, "DATA_LOSS", "Counter upstream response is missing")
	}
	t.result.HTTPStatus[stage] = response.StatusCode
	if stage != "add_context_file" {
		return response, nil
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	_ = response.Body.Close()
	if readErr != nil || len(data) > maxResponseBytes {
		return nil, contract.Fail(502, "DATA_LOSS", "Counter upstream response exceeds its limit")
	}
	// Preserve the original response for core's own file-ID validation. Only
	// known tokenCount and structural field names survive into the report.
	response.Body = io.NopCloser(bytes.NewReader(data))
	inspectCounterResponse(data, t.result)
	return response, nil
}

func structuralObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid field")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate field")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid value")
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid object ending")
	}
	return fields, nil
}

func safeKeys(fields map[string]json.RawMessage) []string {
	keys := []string{}
	for name := range fields {
		if len(name) == 0 || len(name) > 100 {
			continue
		}
		safe := true
		for _, c := range name {
			if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				safe = false
				break
			}
		}
		if safe {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return keys
}

func inspectCounterResponse(raw []byte, result *report) {
	result.CounterParseStatus = "invalid_response_object"
	root, err := structuralObject(raw)
	if err != nil {
		return
	}
	result.ResponseKeys = safeKeys(root)
	result.CounterParseStatus = "missing_add_context_file_response"
	nested, ok := root["addContextFileResponse"]
	if !ok {
		return
	}
	fields, err := structuralObject(nested)
	if err != nil {
		result.CounterParseStatus = "invalid_add_context_file_response"
		return
	}
	result.FileResponseKeys = safeKeys(fields)
	result.CounterParseStatus = "missing_token_count"
	rawCount, ok := fields["tokenCount"]
	if !ok {
		return
	}
	result.CounterParseStatus = "invalid_token_count"
	number := strings.TrimSpace(string(rawCount))
	if strings.HasPrefix(number, `"`) {
		if json.Unmarshal(rawCount, &number) != nil {
			return
		}
	}
	if number == "" {
		return
	}
	for _, c := range number {
		if c < '0' || c > '9' {
			return
		}
	}
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value <= 0 {
		return
	}
	result.TokenCount = &value
	result.CounterParseStatus = "observed_known_token_count"
}

func measure(ctx context.Context, accounts []vault.Account, data []byte, base http.RoundTripper) report {
	hash := sha256.Sum256(data)
	result := report{
		FileSHA256: hex.EncodeToString(hash[:]), FileBytes: len(data),
		CountSource:        "widgetAddContextFile.addContextFileResponse.tokenCount",
		CountingLimitation: "File-ingestion text token count for exact uploaded bytes; not Argon generation or billing usage metadata.",
		HTTPStatus:         map[string]int{}, ResponseKeys: []string{}, FileResponseKeys: []string{},
		CounterParseStatus: "not_observed",
	}
	transport := &counterTransport{base: base, result: &result, sent: map[string]bool{}}
	engine := core.New(ctx, accounts, 1, 180*time.Second, transport)
	defer engine.Close()
	err := engine.Generate(ctx, contract.Selection{Auto: true}, contract.Request{
		Attachments: []contract.Attachment{{Filename: "token-counter.txt", MIMEType: "text/plain", Hash: result.FileSHA256, Data: data}},
	}, func(contract.Event) error {
		result.GenerationEvents++
		return contract.Fail(500, "INTERNAL", "Counter unexpectedly received generation output")
	})
	result.Completed = errors.Is(err, stopBeforeGeneration) && result.GenerationRequestIntercepted && result.GenerationEvents == 0 && result.TokenCount != nil && result.HTTPStatus["add_context_file"] == http.StatusOK
	if !result.Completed {
		result.Failure = "file_ingestion_counter_not_completed"
	}
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("token-count", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	allow := flags.Bool("allow-live", false, "Authorize authentication, session creation and text upload only; no generation")
	vaultPath := flags.String("vault", "", "Existing encrypted local Business vault directory")
	filePath := flags.String("file", "", "Exact synthetic UTF-8 text file to count")
	reportPath := flags.String("report", "", "Sanitized JSON report destination")
	if flags.Parse(args) != nil || !*allow || *vaultPath == "" || *filePath == "" || *reportPath == "" || flags.NArg() != 0 {
		_, _ = io.WriteString(stderr, "Usage: token-count --allow-live --vault <directory> --file <synthetic-text> --report <report.json>\n")
		return 2
	}
	if !separateReportPath(*vaultPath, *filePath, *reportPath) {
		_, _ = io.WriteString(stderr, "Report must be separate from the source text and encrypted vault\n")
		return 2
	}
	file, err := os.Open(*filePath)
	if err != nil {
		_, _ = io.WriteString(stderr, "Synthetic text file could not be opened\n")
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	_ = file.Close()
	if err != nil || len(data) == 0 || len(data) > maxFileBytes || !utf8.Valid(data) {
		_, _ = io.WriteString(stderr, "Synthetic text must be nonempty UTF-8 and at most 50 MiB\n")
		return 1
	}
	snapshot, err := vault.New(*vaultPath).Load()
	if err != nil {
		_, _ = io.WriteString(stderr, "Existing encrypted vault could not be opened\n")
		return 1
	}
	accounts := []vault.Account{}
	for _, account := range snapshot.Accounts {
		if account.Enabled {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) != 1 {
		_, _ = io.WriteString(stderr, "Expected exactly one enabled counter account\n")
		return 1
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The standard transport preserves system TLS certificate verification.
	// core.New's client rejects every redirect with ErrUseLastResponse.
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	result := measure(ctx, accounts, data, transport)
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil || os.MkdirAll(filepath.Dir(*reportPath), 0700) != nil || os.WriteFile(*reportPath, append(encoded, '\n'), 0600) != nil {
		_, _ = io.WriteString(stderr, "Sanitized counter report could not be written\n")
		return 1
	}
	if !result.Completed {
		_, _ = io.WriteString(stderr, "Counter incomplete; inspect the sanitized report\n")
		return 1
	}
	return 0
}

func separateReportPath(vaultPath, filePath, reportPath string) bool {
	vaultAbs, vaultErr := filepath.Abs(vaultPath)
	fileAbs, fileErr := filepath.Abs(filePath)
	reportAbs, reportErr := filepath.Abs(reportPath)
	if vaultErr != nil || fileErr != nil || reportErr != nil || strings.EqualFold(fileAbs, reportAbs) {
		return false
	}
	relative, err := filepath.Rel(vaultAbs, reportAbs)
	if err != nil {
		// Different volumes are necessarily outside the vault directory.
		return filepath.VolumeName(vaultAbs) != filepath.VolumeName(reportAbs)
	}
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
