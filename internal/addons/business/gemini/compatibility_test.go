package gemini

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func TestIgnoredOptionalParametersReachGenerationWithoutBeingForwarded(t *testing.T) {
	b := binding()
	b.GenerationParameters = nil
	for _, optional := range []string{
		`"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_MEDIUM_AND_ABOVE"}]`,
		`"safetySettings":[]`,
		`"safetySettings":null`,
		`"safety_settings":[]`,
		`"systemInstruction":{"parts":[{"text":"ignored-system-text"}]}`,
		`"tools":[{"functionDeclarations":[{"name":"unimplemented_tool"}]}],"toolConfig":{}`,
		`"cachedContent":"cachedContents/unused","labels":{"source":"fixture"}`,
		`"clientSpecificOption":{"enabled":false}`,
		`"model":"other-model","modelId":"other-model","generationConfig":{"modelId":"other-model"}`,
		`"generationConfig":{"temperature":0,"topP":0.5,"maxOutputTokens":128,"thinkingConfig":{"includeThoughts":false},"unknownParameter":null}`,
		`"generation_config":{"top_p":0,"thinking_config":{"include_thoughts":false}}`,
	} {
		t.Run(optional, func(t *testing.T) {
			for _, action := range []string{"generateContent", "streamGenerateContent?alt=sse", "streamGenerateContent"} {
				calls := 0
				g := generatorFunc(func(_ context.Context, selection contract.Selection, request contract.Request, emit func(contract.Event) error) error {
					calls++
					if selection.ModelID != "observed" || selection.Auto {
						t.Fatal("ignored options changed model selection", selection)
					}
					if len(request.Generation) != 0 || len(request.Attachments) != 0 || len(request.QueryParts) != 1 || request.QueryParts[0]["text"] != "hello" {
						t.Fatal("ignored options changed the upstream request", request.Generation, request.QueryParts)
					}
					return emit(contract.Event{Parts: []contract.Part{{Text: "accepted"}}, Finished: true})
				})
				body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],` + optional + `}`
				w := httptest.NewRecorder()
				Handler([]config.Binding{b}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(body)))
				if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), `"finishReason":"STOP"`) {
					t.Fatal(action, w.Code, calls, w.Body.String())
				}
			}
		})
	}
}

func TestIgnoredParametersPreserveVerifiedValuesAndOpaqueSchema(t *testing.T) {
	b := binding()
	b.GenerationParameters["topP"] = config.ParameterBinding{UpstreamField: "topP", Verified: false, Evidence: "unverified fixture"}
	b.GenerationParameters["seed"] = config.ParameterBinding{UpstreamField: "seed", Verified: true}
	body := `{"contents":[{"parts":[{"text":"hello"}]}],"safetySettings":[],"generation_config":{"temperature":0,"top_p":0.7,"seed":42,"thinking_config":{"include_thoughts":false,"thinking_budget":0},"responseSchema":{},"response_json_schema":{"properties":{"keep_snake_case":{"const":9007199254740993}}},"unknownOption":true}}`
	r, err := Parse(strings.NewReader(body), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Generation) != 3 || string(r.Generation["temperature"]) != "0" || !strings.Contains(string(r.Generation["thinkingConfig"]), `"includeThoughts":false`) || !strings.Contains(string(r.Generation["thinkingConfig"]), `"thinkingBudget":0`) || !strings.Contains(string(r.Generation["responseJsonSchema"]), "9007199254740993") || !strings.Contains(string(r.Generation["responseJsonSchema"]), "keep_snake_case") {
		t.Fatal("verified values were changed or ignored values were forwarded", r.Generation)
	}
	if !json.Valid(r.Raw["generation_config"]) || string(r.Raw["safetySettings"]) != "[]" {
		t.Fatal("original wire input lost")
	}
}

func TestUnimplementedMappedGenerationModesAreIgnored(t *testing.T) {
	for _, item := range []struct{ field, value string }{
		{"candidateCount", "2"},
		{"responseModalities", `["IMAGE"]`},
		{"thinkingConfig", `{"madeUpBudget":1}`},
		{"thinkingConfig", `{"includeThoughts":false,"madeUpBudget":null}`},
	} {
		t.Run(item.field, func(t *testing.T) {
			b := binding()
			b.GenerationParameters[item.field] = config.ParameterBinding{UpstreamField: item.field, Verified: true, Evidence: "synthetic fixture"}
			r, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0,"`+item.field+`":`+item.value+`}}`), b)
			if err != nil || len(r.Generation) != 1 || string(r.Generation["temperature"]) != "0" {
				t.Fatal("unsupported optional mode should be omitted", err, r.Generation)
			}
		})
	}
}

func TestIgnoredOptionsDoNotDisableContentOrFormatChecks(t *testing.T) {
	calls := 0
	g := generatorFunc(func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error {
		calls++
		return nil
	})
	for _, body := range []string{
		`{"safetySettings":[]}`,
		`{"contents":[],"safetySettings":null}`,
		`{"contents":[{"parts":[{"text":"hello"}]}],"safetySettings":[],"safetySettings":null}`,
		`{"contents":[{"parts":[{"text":null}]}],"safetySettings":[]}`,
		`{"contents":[{"role":"model","parts":[{"text":"hello"}]}],"safetySettings":[]}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"text/plain","data":"invalid!"}}]}],"safetySettings":[]}`,
		`{"contents":[{"parts":[{"text":"hello"}]}],"safetySettings":[],"generationConfig":"invalid-container"}`,
		`{"contents":[{"parts":[{"text":"hello"}]}],"safetySettings":[],"generationConfig":{"temperature":null}}`,
	} {
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached generation")
	}
}

func TestTwoSupportedSchemasStillConflict(t *testing.T) {
	b := binding()
	b.GenerationParameters["responseSchema"] = config.ParameterBinding{UpstreamField: "responseSchema", Verified: true, Evidence: "fixture"}
	_, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"responseSchema":{},"responseJsonSchema":{}}}`), b)
	if err == nil {
		t.Fatal("conflicting supported schemas accepted")
	}
}
