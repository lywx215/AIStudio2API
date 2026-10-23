package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
)

// Both pinned SDKs exercise the real native handler/parser over JSON pipes.
// The synthetic generator implements only completed-prefix admission; this
// fixture opens no socket and neither imports Host/API nor reads an account.
func TestGeminiSDKPhase5PartsAndChat(t *testing.T) {
	for _, sdk := range []struct {
		name, env, script, version, streamErrors string
	}{
		{"Python", "BUSINESS_GENAI_SDK_PYTHON", "sdk_contract.py", "2.32.0", "raises_502"},
		{"JavaScript", "BUSINESS_GENAI_SDK_NODE", "sdk-js/contract.mjs", "2.22.0", "discarded_without_stop"},
	} {
		t.Run(sdk.name, func(t *testing.T) {
			executable := os.Getenv(sdk.env)
			if executable == "" {
				t.Skip("set " + sdk.env + " to the pinned SDK runtime")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			script, err := filepath.Abs(filepath.Join("../../../../scripts/business-addon", sdk.script))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, script, "--bridge-worker", "--phase5-worker")
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
			generations := 0
			known := make(map[string]bool)
			prefixKey := func(history []string, options string) string {
				return strings.Join(history, ":") + ":" + options
			}
			generator := generatorFunc(func(ctx context.Context, selection contract.Selection, input contract.Request, emit func(contract.Event) error) error {
				if len(input.History) > 1 && !known[prefixKey(input.History[:len(input.History)-1], input.OptionsHash)] {
					return contract.Fail(409, "FAILED_PRECONDITION", "Synthetic unknown completed prefix")
				}
				if selection.ModelID != "observed" || len(input.QueryParts) != 1 || len(input.History) == 0 || len(input.Attachments) != 0 {
					return contract.Fail(500, "INTERNAL", "Unexpected phase 5 fixture input")
				}
				generations++
				prompt := input.QueryParts[0]["text"]
				if prompt == "phase5-fail" {
					for _, text := range []string{"partial-", "failed"} {
						if err := emit(contract.Event{Parts: []contract.Part{{Text: text}}}); err != nil {
							return err
						}
					}
					return contract.Fail(502, "DATA_LOSS", "Synthetic interrupted generation")
				}
				parts := phase5SDKParts("OUTCOME_OK")
				grounding := phase5SDKGrounding()
				if prompt == "phase5-followup" {
					if len(input.History) != 3 {
						return contract.Fail(500, "INTERNAL", "SDK dropped or failed to normalize completed history")
					}
					parts = []contract.Part{{Text: "phase5-followup-ok"}}
					grounding = nil
				} else if strings.HasPrefix(prompt, "phase5-outcome-") {
					outcome := strings.TrimPrefix(prompt, "phase5-outcome-")
					if !contract.ValidExecutionOutcome(outcome) {
						return contract.Fail(500, "INTERNAL", "Unknown fixture outcome")
					}
					parts = phase5SDKParts(outcome)
				} else if prompt != "phase5-unary" && prompt != "phase5-stream" && prompt != "phase5-chat-unary" && prompt != "phase5-chat-stream" {
					return contract.Fail(500, "INTERNAL", "Unknown fixture prompt")
				}
				for _, part := range parts {
					if err := emit(contract.Event{Parts: []contract.Part{part}}); err != nil {
						return err
					}
				}
				if err := emit(contract.Event{Finished: true, GroundingMetadata: grounding}); err != nil {
					return err
				}
				completed := append(append([]string(nil), input.History...), contract.ModelHistoryDigest(parts))
				if !contract.OnDelivery(ctx, func(ok bool) {
					if ok {
						known[prefixKey(completed, input.OptionsHash)] = true
					}
				}) {
					return contract.Fail(500, "INTERNAL", "Handler did not supply delivery admission")
				}
				return nil
			})
			handler := Handler([]config.Binding{binding()}, generator)
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 4096), 4<<20)
			encoder := json.NewEncoder(stdin)
			done, signatureEchoed := false, false
			for scanner.Scan() {
				var wire struct {
					Method, URL, Body string
					Headers           map[string]string
					Done              bool
					Phase             int
					SDK               string
					Requests          int
					Generations       int
					Coverage          []string
					SDKStreamErrors   string
				}
				if err := json.Unmarshal(scanner.Bytes(), &wire); err != nil {
					t.Fatalf("phase 5 SDK bridge emitted invalid JSON: %v", err)
				}
				if wire.Done {
					done = true
					if wire.Phase != 5 || wire.SDK != sdk.version || wire.Requests != 13 || wire.Generations != 11 || wire.Generations != generations || len(wire.Coverage) != 5 || wire.SDKStreamErrors != sdk.streamErrors || !signatureEchoed {
						t.Fatalf("incomplete phase 5 SDK fixture: %+v signatureEchoed=%t", wire, signatureEchoed)
					}
					t.Logf("%s SDK=%s requests=%d generations=%d coverage=%v streamErrors=%s; socket-free real-handler fixture", sdk.name, wire.SDK, wire.Requests, generations, wire.Coverage, wire.SDKStreamErrors)
					break
				}
				u, err := url.Parse(wire.URL)
				if err != nil || u.Scheme != "http" || u.Host != "business-sdk.invalid" || u.User != nil || wire.Method != "POST" || (u.Path != "/v1beta/models/business-test:generateContent" && u.Path != "/v1beta/models/business-test:streamGenerateContent") {
					t.Fatal("phase 5 SDK escaped its offline endpoint")
				}
				body, err := base64.StdEncoding.DecodeString(wire.Body)
				if err != nil || len(body) > 1<<20 {
					t.Fatal("invalid phase 5 bridge body")
				}
				if bytes.Contains(body, []byte("groundingMetadata")) || bytes.Contains(body, []byte("grounding_metadata")) {
					t.Fatal("SDK echoed candidate metadata into conversation content")
				}
				if bytes.Contains(body, []byte("thoughtSignature")) {
					signatureEchoed = signatureEchoed || (sdk.name == "Python" && bytes.Contains(body, []byte("-_8="))) || (sdk.name == "JavaScript" && bytes.Contains(body, []byte("+/8=")))
				}
				r := httptest.NewRequest(wire.Method, wire.URL, bytes.NewReader(body))
				for key, value := range wire.Headers {
					if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-goog-api-key") {
						t.Fatal("phase 5 bridge retained an API credential")
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
				t.Fatalf("phase 5 %s SDK fixture failed: %v\n%s", sdk.name, err, stderr.String())
			}
		})
	}
}

func phase5SDKParts(outcome string) []contract.Part {
	return []contract.Part{
		{Text: "雨A"},
		{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(21 * 2)\n"}, ThoughtSignature: "+/8="},
		{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: outcome, Output: "42\n"}},
		{Text: "fin"},
	}
}

func phase5SDKGrounding() *contract.GroundingMetadata {
	return &contract.GroundingMetadata{
		GroundingChunks:   []contract.GroundingChunk{{Web: &contract.GroundingWeb{URI: "https://example.invalid/source", Title: "Synthetic source"}}},
		GroundingSupports: []contract.GroundingSupport{{Segment: contract.GroundingSegment{PartIndex: 0, StartIndex: 0, EndIndex: 3, Text: "雨"}, GroundingChunkIndices: []int{0}}},
	}
}
