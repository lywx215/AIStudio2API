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

// Both pinned SDKs exercise the real native handler/parser over JSON pipes.
// The synthetic generator implements only completed-prefix admission; this
// fixture opens no socket and neither imports Host/API nor reads an account.
func TestGeminiSDKCompletionMediaFilesAndCatalog(t *testing.T) {
	for _, sdk := range []struct {
		name, env, script, version string
	}{
		{"Python", "BUSINESS_GENAI_SDK_PYTHON", "sdk_contract.py", "2.32.0"},
		{"JavaScript", "BUSINESS_GENAI_SDK_NODE", "sdk-js/contract.mjs", "2.22.0"},
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
			cmd := exec.CommandContext(ctx, executable, script, "--bridge-worker", "--completion-worker")
			cmd.Env = append(os.Environ(), "BUSINESS_SDK_PIPE=1", "BUSINESS_SDK_OUTPUT_DIR="+t.TempDir())
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
			first, second, third := binding(), binding(), binding()
			second.PublicID, third.PublicID = "business-other", "business-third"
			generations := 0
			known := make(map[string]bool)
			prefixKey := func(history []string, options string) string {
				return strings.Join(history, ":") + ":" + options
			}
			generator := generatorFunc(func(ctx context.Context, selection contract.Selection, input contract.Request, emit func(contract.Event) error) error {
				if len(input.History) > 1 && !known[prefixKey(input.History[:len(input.History)-1], input.OptionsHash)] {
					return contract.Fail(409, "FAILED_PRECONDITION", "Synthetic unknown completed prefix")
				}
				if selection.ModelID != "observed" || len(input.QueryParts) != 1 || len(input.History) == 0 {
					return contract.Fail(500, "INTERNAL", "Unexpected completion fixture input")
				}
				generations++
				prompt := input.QueryParts[0]["text"]
				parts := completionSDKParts()
				if prompt == "completion-followup" {
					if len(input.History) != 3 {
						return contract.Fail(500, "INTERNAL", "SDK lost completed media history")
					}
					parts = []contract.Part{{Text: "completion-followup-ok"}}
				} else if prompt == "completion-files" {
					if len(input.Attachments) != 3 {
						return contract.Fail(500, "INTERNAL", "SDK lost file references")
					}
					for i, suffix := range []string{"one", "two", "three"} {
						if string(input.Attachments[i].Data) != "SDK fixture "+suffix {
							return contract.Fail(500, "INTERNAL", "SDK changed file bytes or order")
						}
					}
					parts = []contract.Part{{Text: "completion-files-ok"}}
				} else if prompt != "completion-media" && prompt != "completion-stream" && prompt != "completion-chat" {
					return contract.Fail(500, "INTERNAL", "Unknown completion fixture prompt")
				}
				for _, part := range parts {
					if err := emit(contract.Event{Parts: []contract.Part{part}}); err != nil {
						return err
					}
				}
				if err := emit(contract.Event{Finished: true}); err != nil {
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
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = r.WithContext(contract.WithPrincipal(r.Context(), "offline-completion-owner"))
				if strings.HasPrefix(r.URL.Path, "/upload/") || strings.HasPrefix(r.URL.Path, "/v1beta/files") {
					fileHandler.ServeHTTP(w, r)
					return
				}
				HandlerWithFiles([]config.Binding{first, second, third}, generator, fileHandler.Resolver(r)).ServeHTTP(w, r)
			})
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 4096), 4<<20)
			encoder := json.NewEncoder(stdin)
			done, signatureEchoed := false, false
			for scanner.Scan() {
				var wire struct {
					Method, URL, Body string
					Headers           map[string]string
					Done              bool
					Phase             string
					SDK               string
					Requests          int
					Generations       int
					Coverage          []string
				}
				if err := json.Unmarshal(scanner.Bytes(), &wire); err != nil {
					t.Fatalf("completion SDK bridge emitted invalid JSON: %v", err)
				}
				if wire.Done {
					done = true
					if wire.Phase != "completion" || wire.SDK != sdk.version || wire.Requests < 35 || wire.Generations != 7 || wire.Generations != generations || len(wire.Coverage) != 6 || !signatureEchoed {
						t.Fatalf("incomplete completion SDK fixture: %+v signatureEchoed=%t", wire, signatureEchoed)
					}
					t.Logf("%s SDK=%s requests=%d generations=%d coverage=%v; socket-free real-handler fixture", sdk.name, wire.SDK, wire.Requests, generations, wire.Coverage)
					break
				}
				u, err := url.Parse(wire.URL)
				if err != nil || u.Scheme != "http" || u.Host != "business-sdk.invalid" || u.User != nil {
					t.Fatal("completion SDK escaped its offline endpoint")
				}
				body, err := base64.StdEncoding.DecodeString(wire.Body)
				if err != nil || len(body) > 1<<20 {
					t.Fatal("invalid completion bridge body")
				}
				if bytes.Contains(body, []byte("groundingMetadata")) || bytes.Contains(body, []byte("grounding_metadata")) || bytes.Contains(body, []byte("citationMetadata")) || bytes.Contains(body, []byte("citation_metadata")) {
					t.Fatal("SDK echoed candidate metadata into conversation content")
				}
				if bytes.Contains(body, []byte("thoughtSignature")) {
					signatureEchoed = signatureEchoed || (sdk.name == "Python" && bytes.Contains(body, []byte("-_8="))) || (sdk.name == "JavaScript" && bytes.Contains(body, []byte("+/8=")))
				}
				r := httptest.NewRequest(wire.Method, wire.URL, bytes.NewReader(body))
				for key, value := range wire.Headers {
					if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-goog-api-key") {
						t.Fatal("completion bridge retained an API credential")
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
				t.Fatalf("completion %s SDK fixture failed: %v\n%s", sdk.name, err, stderr.String())
			}
		})
	}
}

func completionSDKParts() []contract.Part {
	data := []byte{251, 255, 0, 1, 10}
	parts := []contract.Part{
		{Text: "media-start"},
		{InlineData: &contract.InlineData{MIMEType: "image/png", Data: data}, Thought: true, ThoughtSignature: "+/8="},
	}
	// Model output fragments have their own part/byte limits; they are not 33
	// user attachments and must survive native Chat's consecutive model turns.
	for range 33 {
		parts = append(parts, contract.Part{InlineData: &contract.InlineData{MIMEType: "audio/pcm;rate=24000", Data: data}})
	}
	return append(parts,
		contract.Part{InlineData: &contract.InlineData{MIMEType: "video/mp4", Data: data}},
		contract.Part{InlineData: &contract.InlineData{MIMEType: "application/pdf", Data: data}},
		contract.Part{Text: "media-end"})
}
