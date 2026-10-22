package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func TestGeminiStreamTerminalErrorDoesNotInventStop(t *testing.T) {
	for _, suffix := range []string{"", "?alt=json", "?alt=sse"} {
		for _, failureAfterFinish := range []bool{false, true} {
			g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
				if err := emit(contract.Event{Parts: []contract.Part{{Text: "partial"}}}); err != nil {
					return err
				}
				if failureAfterFinish {
					if err := emit(contract.Event{Finished: true}); err != nil {
						return err
					}
				}
				return errors.New("internal-sensitive-fixture")
			})
			w := httptest.NewRecorder()
			Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:streamGenerateContent"+suffix, strings.NewReader(prompt)))
			body := w.Body.String()
			if !strings.Contains(body, `"error"`) || strings.Contains(body, "STOP") || strings.Contains(body, "internal-sensitive") {
				t.Fatal(body)
			}
			if suffix != "?alt=sse" && !json.Valid(w.Body.Bytes()) {
				t.Fatal("error corrupted native JSON-array stream", body)
			}
		}
	}
}

func TestGeminiOutputThoughtSignatureAndModelClaims(t *testing.T) {
	g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
		return emit(contract.Event{Parts: []contract.Part{{Text: "thought", Thought: true, ThoughtSignature: "opaque-exact-signature"}}, Finished: true})
	})
	w := httptest.NewRecorder()
	Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(prompt)))
	if !strings.Contains(w.Body.String(), `"thoughtSignature":"opaque-exact-signature"`) || !strings.Contains(w.Body.String(), `"thought":true`) || strings.Contains(w.Body.String(), "modelVersion") || strings.Contains(w.Body.String(), "usageMetadata") {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	Handler([]config.Binding{binding()}, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1beta/models/business-test", nil))
	if strings.Contains(w.Body.String(), "countTokens") || strings.Contains(w.Body.String(), "inputTokenLimit") || strings.Contains(w.Body.String(), "version") || strings.Contains(w.Body.String(), "live_verified") {
		t.Fatal("model catalogue invented unsupported capabilities", w.Body.String())
	}
}

func TestGeminiOutputFailureCannotBeSuppressedByGenerator(t *testing.T) {
	for _, action := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
		g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			_ = emit(contract.Event{Parts: []contract.Part{{Text: strings.Repeat("x", (4<<20)+1)}}, Finished: true})
			return nil
		})
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		if w.Code != 502 || !strings.Contains(w.Body.String(), "RESOURCE_EXHAUSTED") || strings.Contains(w.Body.String(), "STOP") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestGeminiOutputSuggestionsNativeUnionAndBudget(t *testing.T) {
	payload := []byte(`{"suggestions":["synthetic"],"n":9007199254740993}`)
	for _, action := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
		g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			return emit(contract.Event{Parts: []contract.Part{{Text: "answer"}, {InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: payload}}}, Finished: true})
		})
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, `"inlineData":{"mimeType":"application/json+suggestions","data":"`+base64.StdEncoding.EncodeToString(payload)+`"}`) || strings.Contains(body, `"text":""`) {
			t.Fatal(w.Code, body)
		}
	}
	for _, inline := range []*contract.InlineData{
		{MIMEType: "image/png", Data: []byte(`{}`)},
		{MIMEType: contract.SuggestionsMIMEType, Data: []byte(`not JSON`)},
		{MIMEType: contract.SuggestionsMIMEType, Data: []byte(`"` + strings.Repeat("x", 2<<20) + `"`)},
	} {
		g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			return emit(contract.Event{Parts: []contract.Part{{InlineData: inline}}, Finished: true})
		})
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(prompt)))
		if w.Code != 502 || strings.Contains(w.Body.String(), "STOP") {
			t.Fatal("unsafe or oversized suggestions exposed", w.Code, w.Body.String())
		}
	}
}

func TestGeminiUnknownNativeFieldsAndMethodsRejectBeforeGeneration(t *testing.T) {
	calls := 0
	h := Handler([]config.Binding{binding()}, generatorFunc(func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error {
		calls++
		return nil
	}))
	for _, body := range []string{
		`{"contents":[{"parts":[{"functionCall":{"name":"f","args":{}}}]}]}`,
		`{"contents":[{"parts":[{"text":"x","thought":false}]}]}`,
		`{"contents":[{"parts":[{"text":"x"}]}],"systemInstruction":{"parts":[{"text":"s"}]}}`,
		`{"contents":[{"parts":[{"text":"x"}]}],"tools":[]}`,
		`{"contents":[{"parts":[{"text":"x"}]}],"toolConfig":{}}`,
		`{"contents":[{"parts":[{"text":"x"}]}],"cachedContent":"cached/old"}`,
	} {
		for _, action := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(body)))
			if w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_ARGUMENT") || strings.HasPrefix(w.Body.String(), "data:") || strings.HasPrefix(w.Body.String(), "[") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	for _, action := range []string{"countTokens", "predict", "generateImages"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		if w.Code != 501 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("unsupported native fields reached account runtime")
	}
}
