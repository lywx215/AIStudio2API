// Command parameter-probe is an explicit, opt-in diagnostic for the local
// encrypted Business account. It never changes enabled parameter mappings.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type recorder struct {
	base      http.RoundTripper
	fields    map[string]json.RawMessage
	status    int
	unknown   []string
	errorJSON bool
}

func (r *recorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Port() != "" ||
		(request.URL.Host != "business.gemini.google" && request.URL.Host != "biz-discoveryengine.googleapis.com") {
		return nil, contract.Fail(400, "INVALID_ARGUMENT", "Unexpected diagnostic origin")
	}
	isAssist := strings.HasSuffix(request.URL.Path, "/widgetStreamAssist")
	if isAssist {
		body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
		var document struct {
			Assist struct {
				Generation map[string]json.RawMessage `json:"assistGenerationConfig"`
			} `json:"streamAssistRequest"`
		}
		if err = json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		r.fields = document.Assist.Generation
	}
	response, err := r.base.RoundTrip(request)
	if err == nil && isAssist {
		r.status = response.StatusCode
		if response.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			// Only report known parameter names from structured error messages.
			var payload any
			r.errorJSON = json.Unmarshal(body, &payload) == nil
			found := map[string]bool{}
			collectUnknownFields(payload, r.fields, found)
			for field := range found {
				r.unknown = append(r.unknown, field)
			}
			sort.Strings(r.unknown)
		}
	}
	return response, err
}

// Streaming RPC failures may wrap error objects in a JSON array. Traverse
// structured values but expose only names present in our synthetic request.
func collectUnknownFields(value any, requested map[string]json.RawMessage, found map[string]bool) {
	switch item := value.(type) {
	case map[string]any:
		for _, child := range item {
			collectUnknownFields(child, requested, found)
		}
	case []any:
		for _, child := range item {
			collectUnknownFields(child, requested, found)
		}
	case string:
		for field := range requested {
			if strings.Contains(item, "Unknown name \""+field+"\"") {
				found[field] = true
			}
		}
	}
}

func main() {
	dir := flag.String("vault", "", "Existing local encrypted Business vault directory")
	allow := flag.Bool("allow-live", false, "Authorize one real upstream parameter probe")
	flag.Parse()
	if !*allow || *dir == "" {
		os.Exit(2)
	}
	snapshot, err := vault.New(*dir).Load()
	if err != nil {
		println("Existing encrypted vault could not be opened")
		os.Exit(1)
	}
	var accounts []vault.Account
	for _, account := range snapshot.Accounts {
		if account.Enabled {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) != 1 {
		println("Expected one enabled diagnostic account")
		os.Exit(1)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	recorder := &recorder{base: transport, unknown: []string{}}
	engine := core.New(context.Background(), accounts, 1, 90*time.Second, recorder)
	defer engine.Close()
	fields := map[string]json.RawMessage{
		"temperature": json.RawMessage(`0`), "topP": json.RawMessage(`0.5`),
		"topK": json.RawMessage(`1`), "maxOutputTokens": json.RawMessage(`64`),
		"responseMimeType": json.RawMessage(`"application/json"`),
		"thinkingConfig":   json.RawMessage(`{"includeThoughts":false}`),
	}
	events := 0
	err = engine.Generate(context.Background(), contract.Selection{ModelID: "gemini-4-argon"},
		contract.Request{QueryParts: []map[string]string{{"text": "Reply with the JSON object {\"probe\":true}."}}, Generation: fields},
		func(event contract.Event) error { events++; return nil })
	result := map[string]any{"upstream_http_status": recorder.status, "forwarded_generation": recorder.fields,
		"upstream_unknown_fields": recorder.unknown, "events": events, "generation_completed": err == nil,
		"parameter_semantics_verified": false, "configuration_changed": false, "error_json_decoded": recorder.errorJSON}
	if failure, ok := err.(*contract.Failure); ok {
		result["public_status"] = failure.Status
	}
	completed := (recorder.status == 200 && err == nil) || (recorder.status == 400 && len(recorder.unknown) > 0)
	result["diagnostic_completed"] = completed
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if !completed {
		engine.Close()
		os.Exit(1)
	}
}
