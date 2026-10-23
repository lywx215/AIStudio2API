package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func usageFrames(t *testing.T, body string, sse bool) []response {
	t.Helper()
	var frames []response
	if sse {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "data: ") {
				var frame response
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
					t.Fatal(err)
				}
				frames = append(frames, frame)
			}
		}
	} else if strings.HasPrefix(body, "[") {
		if err := json.Unmarshal([]byte(body), &frames); err != nil {
			t.Fatal(err)
		}
	} else {
		var frame response
		if err := json.Unmarshal([]byte(body), &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestLocalUsageIndependentOfStreamingChunkBoundaries(t *testing.T) {
	var expected *contract.Usage
	for _, chunks := range [][]string{{"Hello world! 你好，世界 👋"}, {"Hel", "lo ", "wor", "ld! 你", "好，世界 ", "👋"}} {
		for _, action := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
			t.Run(action+chunks[0], func(t *testing.T) {
				g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
					for _, text := range chunks {
						if err := emit(contract.Event{Parts: []contract.Part{{Text: text}}}); err != nil {
							return err
						}
					}
					return emit(contract.Event{Finished: true})
				})
				var observed []contract.Usage
				ctx := contract.WithGenerationObserver(context.Background(), contract.GenerationObserver{Usage: func(u contract.Usage) { observed = append(observed, u) }})
				w := httptest.NewRecorder()
				Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)).WithContext(ctx))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				frames := usageFrames(t, w.Body.String(), strings.Contains(action, "sse"))
				if len(frames) == 0 {
					t.Fatal("missing output")
				}
				for _, frame := range frames[:len(frames)-1] {
					if frame.UsageMetadata != nil {
						t.Fatal("usage duplicated across deltas")
					}
				}
				u := frames[len(frames)-1].UsageMetadata
				if u == nil || !u.Estimated || u.CountingSource != contract.LocalUsageSource || u.Incomplete || u.PromptTokenCount < 1 || u.CandidatesTokenCount < 1 || u.TotalTokenCount != u.PromptTokenCount+u.CandidatesTokenCount {
					t.Fatalf("invalid local usage: %+v", u)
				}
				if expected == nil {
					expected = u
				} else if *expected != *u {
					t.Fatalf("chunk-dependent usage: want %+v got %+v", *expected, *u)
				}
				if len(observed) != 1 || observed[0] != *u || w.Header().Get("X-AIStudio-Usage-Source") != u.CountingSource {
					t.Fatal("client and recorded usage disagree", observed)
				}
			})
		}
	}
}

func TestLocalUsageValidatedHistoryAndIgnoredOptions(t *testing.T) {
	base, err := Parse(strings.NewReader(prompt), binding())
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := Parse(strings.NewReader(strings.TrimSuffix(prompt, "}")+`,"systemInstruction":{"parts":[{"text":"do not count ignored instructions"}]},"tools":[{"functionDeclarations":[{"name":"ignored"}]}]}`), binding())
	if err != nil || ignored.InputUsage != base.InputUsage {
		t.Fatal("ignored request fields inflated usage", ignored.InputUsage, err)
	}
	history, err := Parse(strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"previous answer"}]},{"role":"user","parts":[{"text":"hello"}]}]}`), binding())
	if err != nil || history.InputUsage.PromptTokenCount <= base.InputUsage.PromptTokenCount {
		t.Fatal("history was not counted", history.InputUsage, err)
	}
	for _, body := range []string{
		`{"contents":[{"parts":[{"text":"hello"},{"inlineData":{"mimeType":"text/plain","data":"aGVsbG8="}}]}]}`,
		`{"contents":[{"parts":[{"text":"hello"},{"inlineData":{"mimeType":"text/plain","data":"` + strings.Repeat("eHh4", 1024) + `"}}]}]}`,
	} {
		r, err := Parse(strings.NewReader(body), binding())
		if err != nil || !r.InputUsage.Incomplete {
			t.Fatal("attachment incorrectly claimed complete usage", r.InputUsage, err)
		}
		// Filename is synthetic and equal, and base64 payload must not count.
		if r.InputUsage.PromptTokenCount > base.InputUsage.PromptTokenCount+32 {
			t.Fatal("counted attachment base64 as model tokens", r.InputUsage)
		}
	}
}

func TestLocalUsageThinkingCodeAndUnknownMedia(t *testing.T) {
	r, err := Parse(strings.NewReader(prompt), binding())
	if err != nil {
		t.Fatal(err)
	}
	parts := []contract.Part{
		{Text: "reasoning", Thought: true, ThoughtSignature: strings.Repeat("opaque", 1000)},
		{Text: "answer"},
		{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}},
		{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "1"}},
		{InlineData: &contract.InlineData{MIMEType: "image/png", Data: []byte("not used for tokenization")}},
	}
	u, err := localOutputUsage(context.Background(), r.InputUsage, parts)
	if err != nil || u.ThoughtsTokenCount < 1 || u.CandidatesTokenCount < 1 || !u.Incomplete || u.TotalTokenCount != u.PromptTokenCount+u.CandidatesTokenCount+u.ThoughtsTokenCount {
		t.Fatal(u, err)
	}
	parts[0].ThoughtSignature = "short"
	other, err := localOutputUsage(context.Background(), r.InputUsage, parts)
	if err != nil || *u != *other {
		t.Fatal("opaque signature changed usage", other, err)
	}
}

func TestLocalUsageFailureAndBudgetDoNotInventSuccess(t *testing.T) {
	calls := 0
	g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
		calls++
		if err := emit(contract.Event{Parts: []contract.Part{{Text: "partial"}}, Finished: true}); err != nil {
			return err
		}
		return errors.New("private failure")
	})
	w := httptest.NewRecorder()
	Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:streamGenerateContent?alt=sse", strings.NewReader(prompt)))
	if strings.Contains(w.Body.String(), "usageMetadata") || strings.Contains(w.Body.String(), "STOP") {
		t.Fatal("failed generation invented final usage", w.Body.String())
	}
	b := resources.New(64 << 10)
	ctx, lease := b.Begin(context.Background())
	w = httptest.NewRecorder()
	Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(prompt)).WithContext(ctx))
	lease.Close()
	if w.Code != 429 || calls != 1 || b.Snapshot().InUseBytes != 0 {
		t.Fatal("tokenizer exceeded admission budget", w.Code, calls, b.Snapshot())
	}
}
