package gemini

import (
	"strings"
	"testing"
)

func TestGoogleSearchParsingAndIgnoredRestrictions(t *testing.T) {
	for _, tc := range []struct {
		tools string
		want  bool
	}{
		{`null`, false}, {`[]`, false},
		{`[{"googleSearch":{}}]`, true},
		{`[{"google_search":{}}]`, true},
		{`[{"codeExecution":{}},{"googleSearch":{}}]`, true},
		{`[{"functionDeclarations":[{"name":"unused"}]}]`, false},
		{`[{"googleSearch":{"timeRangeFilter":{"startTime":"2026-01-01T00:00:00Z"}}}]`, false},
		{`[{"googleSearch":{"searchTypes":{"webSearch":{}}}}]`, false},
		{`[{"googleSearch":{},"codeExecution":{}}]`, false},
	} {
		t.Run(tc.tools, func(t *testing.T) {
			b := binding()
			b.GenerationParameters = nil
			r, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"tools":`+tc.tools+`,"generationConfig":{"temperature":0}}`), b)
			if err != nil || r.GoogleSearch != tc.want || len(r.Generation) != 0 {
				t.Fatalf("search=%v, generation=%v, err=%v", r.GoogleSearch, r.Generation, err)
			}
		})
	}
}

func TestGoogleSearchValidationBeforeGeneration(t *testing.T) {
	for _, raw := range []string{
		`{}`, `[null]`, `[{"googleSearch":null}]`, `[{"googleSearch":true}]`,
		`[{"googleSearch":{},"google_search":{}}]`,
		`[{"googleSearch":{}},{"google_search":{}}]`,
		`[{"googleSearch":{},"googleSearch":{}}]`,
	} {
		if _, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"tools":`+raw+`}`), binding()); err == nil {
			t.Fatal("invalid tools accepted", raw)
		}
	}
}

func TestGoogleSearchOptionChangesHistoryScope(t *testing.T) {
	b := binding()
	plain, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`), b)
	if err != nil {
		t.Fatal(err)
	}
	search, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"tools":[{"googleSearch":{}}]}`), b)
	if err != nil {
		t.Fatal(err)
	}
	if plain.OptionsHash == search.OptionsHash {
		t.Fatal("tool options did not isolate history")
	}
}

func TestGoogleSearchDoesNotDropSharedToolRestrictions(t *testing.T) {
	for _, tc := range []struct {
		config  string
		want    bool
		invalid bool
	}{
		{`"toolConfig":{}`, true, false},
		{`"tool_config":null`, true, false},
		{`"toolConfig":{"retrievalConfig":{"latLng":{"latitude":1,"longitude":2},"languageCode":"fr"}}`, false, false},
		{`"tool_config":{"retrieval_config":{"language_code":"fr"}}`, false, false},
		{`"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}`, false, false},
		{`"toolConfig":{},"tool_config":{}`, false, true},
		{`"toolConfig":[]`, false, true},
	} {
		r, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"tools":[{"googleSearch":{}}],`+tc.config+`}`), binding())
		if (err != nil) != tc.invalid || (!tc.invalid && r.GoogleSearch != tc.want) {
			t.Fatalf("%s: search=%v err=%v", tc.config, r.GoogleSearch, err)
		}
	}
}
