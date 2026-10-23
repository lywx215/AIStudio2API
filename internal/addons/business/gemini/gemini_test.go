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
)

func binding() config.Binding {
	return config.Binding{PublicID: "business-test", DisplayName: "Synthetic", Enabled: true, SelectorMode: "exact", UpstreamModelID: "observed", GenerationParameters: map[string]config.ParameterBinding{
		"temperature": {UpstreamField: "temperature", Verified: true, Evidence: "fixture"}, "thinkingConfig": {UpstreamField: "thinkingConfig", Verified: true, Evidence: "fixture"}, "responseJsonSchema": {UpstreamField: "responseJsonSchema", Verified: true, Evidence: "fixture"},
	}}
}

const prompt = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`

type generatorFunc func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error

func (f generatorFunc) Generate(c context.Context, s contract.Selection, r contract.Request, e func(contract.Event) error) error {
	return f(c, s, r, e)
}

func TestPresenceOpaqueSchemaAndInvalidContentBeforeLease(t *testing.T) {
	r, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0,"thinkingConfig":{"includeThoughts":false,"thinkingBudget":0},"responseJsonSchema":{"type":"object","properties":{"value":{"const":9007199254740993}}}}}`), binding())
	if err != nil || string(r.Generation["temperature"]) != "0" || !strings.Contains(string(r.Generation["thinkingConfig"]), "false") || !strings.Contains(string(r.Generation["responseJsonSchema"]), "9007199254740993") {
		t.Fatal(err)
	}
	calls := 0
	h := Handler([]config.Binding{binding()}, generatorFunc(func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error {
		calls++
		return nil
	}))
	for _, body := range []string{
		`{"contents":[],"contents":[]}`,
		`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"temperature":null}}`,
		`{"contents":[{"role":"model","parts":[{"text":"old"}]},{"role":"user","parts":[{"text":"new"}]}]}`,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("unsupported request leased an account")
	}
}

func TestNativeGenerateAndStreaming(t *testing.T) {
	g := generatorFunc(func(_ context.Context, s contract.Selection, r contract.Request, emit func(contract.Event) error) error {
		if s.ModelID != "observed" || len(r.QueryParts) != 1 {
			t.Fatal("wrong normalized request")
		}
		if err := emit(contract.Event{Parts: []contract.Part{{Text: "hello"}}}); err != nil {
			return err
		}
		return emit(contract.Event{Finished: true})
	})
	for _, action := range []string{"generateContent", "streamGenerateContent?alt=sse", "streamGenerateContent"} {
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"finishReason":"STOP"`) || strings.Contains(w.Body.String(), "choices") || strings.Contains(w.Body.String(), "usageMetadata") {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(action, "sse") {
			if !strings.Contains(w.Body.String(), "data: ") {
				t.Fatal("not SSE")
			}
		} else {
			if !json.Valid(w.Body.Bytes()) {
				t.Fatal("invalid native JSON")
			}
		}
	}
}

func TestInterruptedStreamDoesNotReportSuccessfulCompletion(t *testing.T) {
	g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
		if err := emit(contract.Event{Parts: []contract.Part{{Text: "partial"}}}); err != nil {
			return err
		}
		return errors.New("synthetic-secret-error")
	})
	for _, suffix := range []string{"?alt=sse", ""} {
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:streamGenerateContent"+suffix, strings.NewReader(prompt)))
		body := w.Body.String()
		if !strings.Contains(body, "partial") || !strings.Contains(body, `"error"`) || strings.Contains(body, "STOP") || strings.Contains(body, "synthetic-secret") {
			t.Fatal(body)
		}
	}
}

func TestModelsPagination(t *testing.T) {
	b := binding()
	other := b
	other.PublicID = "business-other"
	h := Handler([]config.Binding{b, other}, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1beta/models?pageSize=1", nil))
	var page struct {
		Models []json.RawMessage `json:"models"`
		Next   string            `json:"nextPageToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Models) != 1 || page.Next == "" {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1beta/models?pageSize=1&pageToken="+page.Next, nil))
	if !strings.Contains(w.Body.String(), "business-other") || strings.Contains(w.Body.String(), "nextPageToken") {
		t.Fatal(w.Body.String())
	}
}
