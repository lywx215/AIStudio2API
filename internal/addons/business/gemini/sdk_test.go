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

// This fixture is deliberately socket-free. The actual pinned Python SDK uses
// httpx.MockTransport, which exchanges wire requests/responses through pipes.
// Set BUSINESS_GENAI_SDK_PYTHON to a Python with requirements-sdk.txt installed.
func TestGeminiSDKOfflineHarness(t *testing.T) {
	python := os.Getenv("BUSINESS_GENAI_SDK_PYTHON")
	if python == "" {
		t.Skip("set BUSINESS_GENAI_SDK_PYTHON to run the pinned google-genai==2.32.0 offline fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script, err := filepath.Abs("../../../../scripts/business-addon/sdk_contract.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, python, script, "--bridge-worker")
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
	generator := generatorFunc(func(_ context.Context, _ contract.Selection, input contract.Request, emit func(contract.Event) error) error {
		generations++
		if len(input.QueryParts) == 0 && len(input.Attachments) == 0 {
			return contract.Fail(400, "INVALID_ARGUMENT", "empty fixture request")
		}
		if len(input.Generation) > 0 {
			if string(input.Generation["temperature"]) != "0.0" || !strings.Contains(string(input.Generation["thinkingConfig"]), "false") || !strings.Contains(string(input.Generation["thinkingConfig"]), "thinkingBudget") || !strings.Contains(string(input.Generation["responseJsonSchema"]), "9007199254740993") || !strings.Contains(string(input.Generation["responseJsonSchema"]), "keep_snake_case") {
				return contract.Fail(500, "INTERNAL", "SDK fixture lost field presence or opaque schema")
			}
		}
		if len(input.QueryParts) > 0 && input.QueryParts[0]["text"] == "thought" {
			if err := emit(contract.Event{Parts: []contract.Part{{Text: "reasoning", Thought: true, ThoughtSignature: base64.StdEncoding.EncodeToString([]byte("opaque-fixture"))}}}); err != nil {
				return err
			}
		}
		if len(input.QueryParts) > 0 && input.QueryParts[0]["text"] == "suggestions" {
			if err := emit(contract.Event{Parts: []contract.Part{{InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: []byte(`{"suggestions":["synthetic"],"enabled":false}`)}}}}); err != nil {
				return err
			}
		}
		if err := emit(contract.Event{Parts: []contract.Part{{Text: "fixture-"}}}); err != nil {
			return err
		}
		return emit(contract.Event{Parts: []contract.Part{{Text: "output"}}, Finished: true})
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(contract.WithPrincipal(r.Context(), "offline-sdk-owner"))
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
		}
		if err := json.Unmarshal(scanner.Bytes(), &wire); err != nil {
			t.Fatalf("SDK bridge emitted invalid JSON: %v", err)
		}
		if wire.Done {
			done = true
			if wire.SDK != "2.32.0" || wire.Requests < 20 || wire.Generations != generations {
				t.Fatalf("incomplete SDK fixture: %+v", wire)
			}
			break
		}
		if !strings.HasPrefix(wire.URL, "http://business-sdk.invalid/") {
			t.Fatal("SDK request escaped the offline origin")
		}
		body, err := base64.StdEncoding.DecodeString(wire.Body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(wire.Method, wire.URL, bytes.NewReader(body))
		for key, value := range wire.Headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code >= 400 && strings.HasPrefix(r.URL.Path, "/upload/") {
			t.Logf("offline SDK upload rejected: method=%s path=%s header=%v body=%s", r.Method, r.URL.Path, r.Header, body)
		}
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
		t.Fatalf("pinned SDK fixture failed: %v\n%s", err, stderr.String())
	}
	t.Logf("google-genai==2.32.0 exercised real Gemini/file handlers via MockTransport; generations=%d; no network sockets", generations)
}
