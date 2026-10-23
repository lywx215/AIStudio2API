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

type phase5OutputFrame struct {
	Candidates []struct {
		Content struct {
			Parts []contract.Part `json:"parts"`
		} `json:"content"`
		FinishReason string                      `json:"finishReason"`
		Grounding    *contract.GroundingMetadata `json:"groundingMetadata"`
	} `json:"candidates"`
	Error json.RawMessage `json:"error"`
}

func phase5ReadOutput(t *testing.T, body, action string) []phase5OutputFrame {
	t.Helper()
	var frames []phase5OutputFrame
	if strings.Contains(action, "alt=sse") {
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame phase5OutputFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				t.Fatal("invalid SSE data JSON", err)
			}
			frames = append(frames, frame)
		}
	} else if strings.HasPrefix(action, "streamGenerateContent") {
		if err := json.Unmarshal([]byte(body), &frames); err != nil {
			t.Fatal("invalid array stream", err)
		}
	} else {
		var frame phase5OutputFrame
		if err := json.Unmarshal([]byte(body), &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func phase5Grounding() *contract.GroundingMetadata {
	return &contract.GroundingMetadata{
		GroundingChunks:   []contract.GroundingChunk{{Web: &contract.GroundingWeb{URI: "https://example.test/source", Title: "source"}}},
		GroundingSupports: []contract.GroundingSupport{{Segment: contract.GroundingSegment{PartIndex: 0, StartIndex: 1, EndIndex: 7, Text: "你好"}, GroundingChunkIndices: []int{0}}},
	}
}

func TestPhase5OutputSingleNativePartHasRealSuccessfulLastFrame(t *testing.T) {
	for _, action := range []string{"streamGenerateContent", "streamGenerateContent?alt=json", "streamGenerateContent?alt=sse"} {
		for _, last := range []contract.Part{
			{Text: "one-original-part"},
			{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}},
			{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "1\n"}},
		} {
			t.Run(action+string(mustPhase5Marshal(t, last)), func(t *testing.T) {
				w := httptest.NewRecorder()
				calls, delivered := 0, false
				g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
					if !contract.OnDelivery(ctx, func(ok bool) { calls++; delivered = ok }) {
						t.Fatal("missing complete-delivery contract")
					}
					if err := emit(contract.Event{Parts: []contract.Part{last}}); err != nil {
						return err
					}
					if w.Body.Len() != 0 || calls != 0 {
						t.Fatal("last original part was released or committed prematurely")
					}
					return emit(contract.Event{Finished: true})
				})
				Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
				frames := phase5ReadOutput(t, w.Body.String(), action)
				if w.Code != 200 || calls != 1 || !delivered || len(frames) != 1 || len(frames[0].Candidates) != 1 {
					t.Fatal("single-part completion lost", w.Code, calls, delivered, w.Body.String())
				}
				candidate := frames[0].Candidates[0]
				if candidate.FinishReason != "STOP" || len(candidate.Content.Parts) != 1 || string(mustPhase5Marshal(t, candidate.Content.Parts[0])) != string(mustPhase5Marshal(t, last)) {
					t.Fatal("terminal did not carry exactly the original native part", w.Body.String())
				}
			})
		}
	}
}

func mustPhase5Marshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestPhase5OutputPendingFailureDoesNotStopOrCommit(t *testing.T) {
	for _, action := range []string{"streamGenerateContent?alt=json", "streamGenerateContent?alt=sse"} {
		for _, finished := range []bool{false, true} {
			calls, delivered := 0, false
			g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
				contract.OnDelivery(ctx, func(ok bool) { calls++; delivered = ok })
				if err := emit(contract.Event{Parts: []contract.Part{{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "pending-original"}}}}); err != nil {
					return err
				}
				if finished {
					if err := emit(contract.Event{Finished: true}); err != nil {
						return err
					}
				}
				return errors.New("synthetic private failure")
			})
			w := httptest.NewRecorder()
			Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
			frames := phase5ReadOutput(t, w.Body.String(), action)
			if calls != 1 || delivered || len(frames) != 2 || len(frames[1].Error) == 0 || strings.Contains(w.Body.String(), "STOP") || strings.Contains(w.Body.String(), "private failure") || strings.Count(w.Body.String(), "pending-original") != 1 {
				t.Fatal("pending part failure certified or lost original partial content", calls, delivered, w.Body.String())
			}
		}
	}
}

type phase5FlushFailureWriter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *phase5FlushFailureWriter) FlushError() error {
	w.flushes++
	return errors.New("synthetic terminal flush failure")
}

func TestPhase5OutputTerminalFlushFailureNeverResendsOriginalPart(t *testing.T) {
	for _, action := range []string{"streamGenerateContent?alt=sse", "streamGenerateContent?alt=json"} {
		w := &phase5FlushFailureWriter{ResponseRecorder: httptest.NewRecorder()}
		calls, commits := 0, 0
		g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			contract.OnDelivery(ctx, func(ok bool) {
				calls++
				if ok {
					commits++
				}
			})
			if err := emit(contract.Event{Parts: []contract.Part{{Text: "terminal-original-once"}}}); err != nil {
				return err
			}
			return emit(contract.Event{Finished: true})
		})
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		if strings.Count(w.Body.String(), "terminal-original-once") != 1 || w.flushes != 1 || calls != 1 || commits != 0 {
			t.Fatal("failed terminal flush repeated content or committed prefix", w.flushes, calls, commits, w.Body.String())
		}
		// The original STOP bytes were written before FlushError. They cannot
		// be recalled, but no second content frame or successful delivery is valid.
	}
}

func TestPhase5OutputGroundingValidatesUTF8AndReferenceBoundaries(t *testing.T) {
	parts := []contract.Part{{Text: "x你好z", ThoughtSignature: "opaque"}}
	for _, action := range []string{"generateContent", "streamGenerateContent?alt=sse"} {
		g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			if err := emit(contract.Event{Parts: parts}); err != nil {
				return err
			}
			return emit(contract.Event{Finished: true, GroundingMetadata: phase5Grounding()})
		})
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		frames := phase5ReadOutput(t, w.Body.String(), action)
		if w.Code != 200 || len(frames) != 1 || len(frames[0].Candidates) != 1 || frames[0].Candidates[0].FinishReason != "STOP" || frames[0].Candidates[0].Grounding == nil {
			t.Fatal("valid final UTF8-byte grounding lost", w.Body.String())
		}
	}
	for name, mutate := range map[string]func(*contract.GroundingMetadata, []contract.Part){
		"split-rune-start": func(g *contract.GroundingMetadata, _ []contract.Part) { g.GroundingSupports[0].Segment.StartIndex = 2 },
		"split-rune-end":   func(g *contract.GroundingMetadata, _ []contract.Part) { g.GroundingSupports[0].Segment.EndIndex = 6 },
		"wrong-text": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingSupports[0].Segment.Text = "changed"
		},
		"part-out-of-bounds": func(g *contract.GroundingMetadata, _ []contract.Part) { g.GroundingSupports[0].Segment.PartIndex = 1 },
		"negative-source": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingSupports[0].GroundingChunkIndices = []int{-1}
		},
		"missing-source": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingSupports[0].GroundingChunkIndices = []int{1}
		},
		"empty-source-list": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingSupports[0].GroundingChunkIndices = nil
		},
		"thought-part": func(_ *contract.GroundingMetadata, p []contract.Part) { p[0].Thought = true },
		"typed-part": func(_ *contract.GroundingMetadata, p []contract.Part) {
			p[0] = contract.Part{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}}
		},
		"userinfo-uri": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingChunks[0].Web.URI = "https://user:password@example.test/source"
		},
		"non-http-uri": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingChunks[0].Web.URI = "file:///private"
		},
		"invalid-title-utf8": func(g *contract.GroundingMetadata, _ []contract.Part) {
			g.GroundingChunks[0].Web.Title = string([]byte{0xff})
		},
	} {
		t.Run(name, func(t *testing.T) {
			metadata := phase5Grounding()
			changed := append([]contract.Part(nil), parts...)
			mutate(metadata, changed)
			calls, delivered := 0, false
			g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
				contract.OnDelivery(ctx, func(ok bool) { calls++; delivered = ok })
				// Even a generator that suppresses emit's validation error must
				// not turn malformed final metadata into a successful response.
				_ = emit(contract.Event{Parts: changed, Finished: true, GroundingMetadata: metadata})
				return nil
			})
			w := httptest.NewRecorder()
			Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(prompt)))
			if w.Code != 502 || calls != 1 || delivered || strings.Contains(w.Body.String(), "STOP") || !strings.Contains(w.Body.String(), "DATA_LOSS") {
				t.Fatal("invalid final grounding became successful", w.Code, calls, delivered, w.Body.String())
			}
		})
	}
}

func TestPhase5OutputGroundingCannotPrecedeCompletion(t *testing.T) {
	g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
		return emit(contract.Event{Parts: []contract.Part{{Text: "x你好z"}}, GroundingMetadata: phase5Grounding()})
	})
	w := httptest.NewRecorder()
	Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:streamGenerateContent?alt=sse", strings.NewReader(prompt)))
	if w.Code != 502 || strings.Contains(w.Body.String(), "groundingChunks") || strings.Contains(w.Body.String(), "STOP") {
		t.Fatal("nonfinal grounding was published", w.Code, w.Body.String())
	}
}

func TestPhase5OutputNativeEncodingAndGroundingBudgetRelease(t *testing.T) {
	budget := resources.New(4096)
	ctx, lease := budget.Begin(context.Background())
	defer lease.Close()
	parts := []contract.Part{{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: strings.Repeat("\x00", 1024)}}}
	if _, _, err := marshalResponse(ctx, parts, true); !errors.Is(err, resources.ErrExhausted) || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("failed native encode retained reservation", err, budget.Snapshot())
	}
	budget.SetLimit(4 << 20)
	encoded, release, err := marshalResponse(ctx, parts, true)
	if err != nil || !json.Valid(encoded) || budget.Snapshot().InUseBytes == 0 {
		t.Fatal("native encoding did not reserve worst-case escaped strings", err)
	}
	release()
	release()
	if budget.Snapshot().InUseBytes != 0 {
		t.Fatal("native encoding release leaked or was not idempotent")
	}
	if _, _, err := marshalResponse(ctx, []contract.Part{{Text: "conflict", ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "x"}}}, true); err == nil || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("native marshal error leaked its buffer reservation", err)
	}
	metadata := phase5Grounding()
	if _, err := validateOutputGrounding(ctx, metadata, []contract.Part{{Text: "x你好z"}}); err != nil || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("grounding validation retained temporary encoded-buffer reservation", err)
	}
	budget.SetLimit(0)
	if _, err := validateOutputGrounding(ctx, metadata, []contract.Part{{Text: "x你好z"}}); !errors.Is(err, resources.ErrExhausted) || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("grounding budget failure changed classification or leaked", err)
	}
}
