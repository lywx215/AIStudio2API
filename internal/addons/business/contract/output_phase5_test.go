package contract

import (
	"encoding/json"
	"testing"
)

func TestPhase5ContractNativePartUnionDoesNotInventText(t *testing.T) {
	for _, test := range []struct {
		kind string
		part Part
	}{
		{"text", Part{Text: ""}},
		{"inlineData", Part{InlineData: &InlineData{MIMEType: SuggestionsMIMEType, Data: []byte(`{}`)}}},
		{"executableCode", Part{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(1)"}, Thought: true, ThoughtSignature: "opaque-code"}},
		{"codeExecutionResult", Part{CodeExecutionResult: &CodeExecutionResult{Outcome: "OUTCOME_OK"}, ThoughtSignature: "opaque-result"}},
	} {
		t.Run(test.kind, func(t *testing.T) {
			encoded, err := json.Marshal(test.part)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"text", "inlineData", "executableCode", "codeExecutionResult", "functionCall", "functionResponse"} {
				_, present := wire[kind]
				if present != (kind == test.kind) {
					t.Fatal("native union changed", kind, string(encoded))
				}
			}
			if test.part.ThoughtSignature != "" && string(wire["thoughtSignature"]) != `"`+test.part.ThoughtSignature+`"` {
				t.Fatal("signature detached from native part")
			}
		})
	}
	for _, part := range []Part{
		{Text: "x", ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "x"}},
		{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "x"}, CodeExecutionResult: &CodeExecutionResult{Outcome: "OUTCOME_OK"}},
		{InlineData: &InlineData{Data: []byte(`{}`)}, CodeExecutionResult: &CodeExecutionResult{Outcome: "OUTCOME_OK"}},
	} {
		if _, err := json.Marshal(part); err == nil {
			t.Fatal("conflicting native values were encoded")
		}
	}
}

func TestPhase5ContractCodeHistoryIdentityAndBoundaries(t *testing.T) {
	makeParts := func() []Part {
		return []Part{
			{Text: "before"},
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(1)"}, ThoughtSignature: "opaque-code"},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "1\n"}},
			{Text: "after"},
		}
	}
	base := makeParts()
	want := ModelHistoryDigest(base)
	before, after := "before", "after"
	if HistoryDigest("model", []HistoryPart{
		{Text: &before}, {ExecutableCode: base[1].ExecutableCode, ThoughtSignature: "opaque-code"},
		{CodeExecutionResult: base[2].CodeExecutionResult}, {Text: &after},
	}) != want {
		t.Fatal("parser and generated native part identities differ")
	}
	for name, mutate := range map[string]func([]Part){
		"code":         func(p []Part) { p[1].ExecutableCode.Code = "print(2)" },
		"language":     func(p []Part) { p[1].ExecutableCode.Language = "OTHER" },
		"outcome":      func(p []Part) { p[2].CodeExecutionResult.Outcome = "OUTCOME_FAILED" },
		"output":       func(p []Part) { p[2].CodeExecutionResult.Output = "2\n" },
		"signature":    func(p []Part) { p[1].ThoughtSignature = "opaque-other" },
		"thought":      func(p []Part) { p[1].Thought = true },
		"reorder":      func(p []Part) { p[1], p[2] = p[2], p[1] },
		"flatten-code": func(p []Part) { p[1] = Part{Text: "print(1)"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := makeParts()
			mutate(changed)
			if ModelHistoryDigest(changed) == want {
				t.Fatal("changed native history retained completed-prefix identity")
			}
		})
	}
	split := append([]Part{{Text: "be"}, {Text: "fore"}}, base[1:]...)
	if ModelHistoryDigest(split) != want {
		t.Fatal("ordinary streaming text segmentation changed identity")
	}
	moved := []Part{{Text: "beforeafter"}, base[1], base[2]}
	if ModelHistoryDigest(moved) == want {
		t.Fatal("ordinary text was merged across native code boundaries")
	}
}
