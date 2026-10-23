package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/fileapi"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/files"
)

// The pinned official JavaScript SDK sends fetch calls through pipes to real handlers.
// It does not mount Host/API, authenticate a real account or open any socket.
func TestGeminiJavaScriptSDKOfflineHarness(t *testing.T) {
	node := os.Getenv("BUSINESS_GENAI_SDK_NODE")
	if node == "" {
		t.Skip("set BUSINESS_GENAI_SDK_NODE after npm ci in scripts/business-addon/sdk-js")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script, err := filepath.Abs("../../../../scripts/business-addon/sdk-js/contract.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, script, "--bridge-worker")
	cmd.Env = append(os.Environ(), "BUSINESS_SDK_PIPE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = stdin.Close(); _ = cmd.Wait() }()
	fileHandler := fileapi.New(files.New(t.TempDir()))
	first, second := binding(), binding()
	second.PublicID = "business-other"
	generations := 0
	generator := generatorFunc(func(_ context.Context, selection contract.Selection, input contract.Request, emit func(contract.Event) error) error {
		if len(input.History) > 1 {
			// This fixture has no real Core session cache. A syntactically valid
			// history must survive SDK/handler parsing, then fail prefix lookup.
			return contract.Fail(409, "FAILED_PRECONDITION", "Synthetic unknown history prefix")
		}
		generations++
		if selection.ModelID != "observed" || (len(input.QueryParts) == 0 && len(input.Attachments) == 0) {
			return contract.Fail(500, "INTERNAL", "Invalid JS fixture input")
		}
		text := ""
		if len(input.QueryParts) > 0 {
			text = input.QueryParts[0]["text"]
		}
		if text == "mapped" {
			if string(input.Generation["temperature"]) != "0" || !strings.Contains(string(input.Generation["thinkingConfig"]), `"includeThoughts":false`) || !strings.Contains(string(input.Generation["thinkingConfig"]), `"thinkingBudget":0`) || !strings.Contains(string(input.Generation["responseJsonSchema"]), "9007199254740991") || !strings.Contains(string(input.Generation["responseJsonSchema"]), "keep_snake_case") {
				return contract.Fail(500, "INTERNAL", "JS SDK lost mapped field values")
			}
		} else if len(input.Generation) != 0 {
			return contract.Fail(500, "INTERNAL", "Ignored parameters reached generator")
		}
		if text == "file reference" && (len(input.Attachments) != 1 || string(input.Attachments[0].Data) != "synthetic PDF file bytes") {
			return contract.Fail(500, "INTERNAL", "JS SDK file snapshot was not preserved")
		}
		if text == "fail" {
			return contract.Fail(503, "UNAVAILABLE", "Synthetic unavailable")
		}
		if text == "midfail" || text == "truncated" {
			if err := emit(contract.Event{Parts: []contract.Part{{Text: "partial"}}}); err != nil {
				return err
			}
			if text == "midfail" {
				return contract.Fail(502, "DATA_LOSS", "Synthetic stream error")
			}
			return nil // Handler must reject a stream without a terminal event.
		}
		if text == "thought" {
			if err := emit(contract.Event{Parts: []contract.Part{{Text: "reasoning", Thought: true, ThoughtSignature: base64.StdEncoding.EncodeToString([]byte("opaque-fixture"))}}}); err != nil {
				return err
			}
		}
		if text == "suggestions" {
			if err := emit(contract.Event{Parts: []contract.Part{{InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: []byte(`{"suggestions":["synthetic"],"enabled":false}`)}}}}); err != nil {
				return err
			}
		}
		if err := emit(contract.Event{Parts: []contract.Part{{Text: "fixture-"}}}); err != nil {
			return err
		}
		return emit(contract.Event{Parts: []contract.Part{{Text: "输出"}}, Finished: true})
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(contract.WithPrincipal(r.Context(), "offline-js-sdk-owner"))
		if strings.HasPrefix(r.URL.Path, "/upload/") || strings.HasPrefix(r.URL.Path, "/v1beta/files") {
			fileHandler.ServeHTTP(w, r)
			return
		}
		HandlerWithFiles([]config.Binding{first, second}, generator, fileHandler.Resolver(r)).ServeHTTP(w, r)
	})
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	encoder := json.NewEncoder(stdin)
	done := false
	for scanner.Scan() {
		var wire struct {
			Method, URL, Body string
			Headers           map[string]string
			Done              bool
			SDK               string
			Requests          int
			Generations       int
			Coverage          []string
			SDKStreamErrors   string
		}
		if err := json.Unmarshal(scanner.Bytes(), &wire); err != nil {
			t.Fatalf("JS bridge emitted invalid JSON: %v", err)
		}
		if wire.Done {
			done = true
			if wire.SDK != "2.22.0" || wire.Requests < 25 || wire.Generations != generations || len(wire.Coverage) != 9 || wire.SDKStreamErrors != "discarded_without_stop" {
				t.Fatalf("incomplete JS SDK fixture: %+v", wire)
			}
			t.Logf("@google/genai=%s requests=%d generations=%d coverage=%v; socket-free real-handler fixture", wire.SDK, wire.Requests, wire.Generations, wire.Coverage)
			t.Log("Pinned SDK drops SSE error fields; consumer requires STOP and rejects incomplete output without replay")
			break
		}
		u, err := url.Parse(wire.URL)
		if err != nil || u.Scheme != "http" || u.Host != "business-sdk.invalid" || u.User != nil {
			t.Fatal("JS SDK escaped offline origin")
		}
		body, err := base64.StdEncoding.DecodeString(wire.Body)
		if err != nil || len(body) > 1<<20 {
			t.Fatal("invalid JS bridge body")
		}
		r := httptest.NewRequest(wire.Method, wire.URL, bytes.NewReader(body))
		for key, value := range wire.Headers {
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-goog-api-key") {
				t.Fatal("JS bridge retained an API credential")
			}
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		headers["X-Fixture-Generations"] = fmt.Sprint(generations)
		if err := encoder.Encode(map[string]any{"status": w.Code, "headers": headers, "body": base64.StdEncoding.EncodeToString(w.Body.Bytes())}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil || !done {
		t.Fatalf("pinned JS SDK fixture failed: %v\n%s", err, stderr.String())
	}
}
