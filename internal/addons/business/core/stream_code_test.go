package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

// These are synthetic examples of the documented Enterprise union, not proof
// that the current widget/account emitted code or enabled a code tool.
func TestStreamCodeAndExecutionResultsKeepNativeUnion(t *testing.T) {
	code := "print('你好')\n"
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "before"}, map[string]any{
			"role": "model", "executableCode": map[string]any{"code": code},
			"thought": true, "thoughtSignature": "opaque-exact+/=",
		}),
		liveFrame("IN_PROGRESS", map[string]any{"codeExecutionResult": map[string]any{"outcome": "OUTCOME_OK", "output": "你好\n"}, "thoughtSignature": "result-signature"}),
		liveFrame("SUCCEEDED", map[string]any{"text": "after"}),
	)
	if err != nil || !finished || len(parts) != 4 {
		t.Fatal("native code sequence lost", parts, finished, err)
	}
	if parts[1].ExecutableCode == nil || *parts[1].ExecutableCode != (contract.ExecutableCode{Language: "PYTHON", Code: code}) || !parts[1].Thought || parts[1].ThoughtSignature != "opaque-exact+/=" {
		t.Fatal("code or annotations changed", parts[1])
	}
	if parts[2].CodeExecutionResult == nil || *parts[2].CodeExecutionResult != (contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "你好\n"}) || parts[2].ThoughtSignature != "result-signature" {
		t.Fatal("execution result or signature changed", parts[2])
	}
	for _, index := range []int{1, 2} {
		encoded, err := json.Marshal(parts[index])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for _, invented := range []string{"text", "functionCall", "functionResponse", "inlineData"} {
			if _, exists := fields[invented]; exists {
				t.Fatal("typed output acquired an unrelated value", invented)
			}
		}
	}
	for _, outcome := range []string{"OUTCOME_OK", "OUTCOME_FAILED", "OUTCOME_DEADLINE_EXCEEDED"} {
		parts, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", map[string]any{"codeExecutionResult": map[string]any{"outcome": outcome}}))
		if err != nil || !finished || len(parts) != 1 || parts[0].CodeExecutionResult == nil || *parts[0].CodeExecutionResult != (contract.CodeExecutionResult{Outcome: outcome}) {
			t.Fatal("valid execution result with omitted output rejected", outcome, parts, finished, err)
		}
	}
}

func TestStreamCodeRejectsUnknownAndMalformedValues(t *testing.T) {
	for _, test := range []struct {
		name, content string
		reason        streamDiagnostic
	}{
		{"code-null", `{"executableCode":null}`, streamInvalidExecutableCode},
		{"code-empty-object", `{"executableCode":{}}`, streamInvalidExecutableCode},
		{"code-empty-string", `{"executableCode":{"code":""}}`, streamInvalidExecutableCode},
		{"code-array", `{"executableCode":[]}`, streamInvalidExecutableCode},
		{"code-value-null", `{"executableCode":{"code":null}}`, streamInvalidExecutableCode},
		{"code-value-number", `{"executableCode":{"code":1}}`, streamInvalidExecutableCode},
		{"language-not-on-enterprise-wire", `{"executableCode":{"code":"x","language":"PYTHON"}}`, streamUnsupportedExecutableCode},
		{"unknown-code-id", `{"executableCode":{"code":"x","id":"private-id"}}`, streamUnsupportedExecutableCode},
		{"result-null", `{"codeExecutionResult":null}`, streamInvalidCodeExecutionResult},
		{"missing-outcome", `{"codeExecutionResult":{"output":"x"}}`, streamInvalidCodeExecutionResult},
		{"unspecified-outcome", `{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED"}}`, streamInvalidCodeExecutionResult},
		{"unknown-outcome", `{"codeExecutionResult":{"outcome":"private-status"}}`, streamInvalidCodeExecutionResult},
		{"numeric-outcome", `{"codeExecutionResult":{"outcome":1}}`, streamInvalidCodeExecutionResult},
		{"null-output", `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":null}}`, streamInvalidCodeExecutionResult},
		{"structured-output", `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":{"private":"x"}}}`, streamInvalidCodeExecutionResult},
		{"unknown-result-id", `{"codeExecutionResult":{"outcome":"OUTCOME_OK","id":"private-id"}}`, streamUnsupportedCodeExecutionResult},
		{"mixed-text-code", `{"text":"","executableCode":{"code":"x"}}`, streamMixedContentPart},
		{"mixed-code-result", `{"executableCode":{"code":"x"},"codeExecutionResult":{"outcome":"OUTCOME_OK"}}`, streamMixedContentPart},
		{"mixed-inline-code", `{"inlineData":{"mimeType":"application/json+suggestions","data":"e30="},"executableCode":{"code":"x"}}`, streamMixedInlinePart},
		{"thought-null", `{"executableCode":{"code":"x"},"thought":null}`, streamInvalidThought},
		{"signature-null", `{"codeExecutionResult":{"outcome":"OUTCOME_OK"},"thoughtSignature":null}`, streamInvalidSignature},
		{"duplicate-code-key", `{"executableCode":{"code":"first","code":"second"}}`, streamDuplicateJSONKey},
		{"invalid-code-unicode", `{"executableCode":{"code":"\ud800"}}`, streamInvalidUnicodeEscape},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":` + test.content + `}}]}}}]`
			finished := false
			err := decodeStream(strings.NewReader(body), func(event contract.Event) error { finished = event.Finished; return nil })
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Code != 502 || finished || failure.Message != "Business response is malformed or incomplete ("+string(test.reason)+")" {
				t.Fatal("malformed or unsupported typed content was accepted or leaked", finished, err)
			}
		})
	}
}

func TestStreamCodeDisablesTextOnlyTerminalGuessing(t *testing.T) {
	code := map[string]any{"executableCode": map[string]any{"code": "print(1)"}}
	result := map[string]any{"codeExecutionResult": map[string]any{"outcome": "OUTCOME_OK", "output": "1\n"}}
	for _, typed := range []map[string]any{code, result} {
		for _, terminalContainsTyped := range []bool{false, true} {
			first := []map[string]any{{"text": "prefix"}}
			last := []map[string]any{{"text": "prefix suffix"}}
			if terminalContainsTyped {
				last = append([]map[string]any{typed}, last...)
			} else {
				first = append(first, typed)
			}
			parts, finished, err := decodeLiveFixture(t, liveFrame("IN_PROGRESS", first...), liveFrame("SUCCEEDED", last...))
			if err != nil || !finished || len(parts) != 3 || parts[2].Text != "prefix suffix" || (parts[1].ExecutableCode == nil && parts[1].CodeExecutionResult == nil) {
				t.Fatal("terminal guessing removed typed data or crossed a typed boundary", parts, finished, err)
			}
		}
	}
	// An exact full typed prefix is the necessary evidence for stripping a
	// cumulative terminal copy. A partial/type-reordered prefix above is not.
	parts, finished, err := decodeLiveFixture(t, liveFrame("IN_PROGRESS", code), liveFrame("SUCCEEDED", code))
	if err != nil || !finished || len(parts) != 1 || parts[0].ExecutableCode == nil {
		t.Fatal("complete native terminal prefix was duplicated", parts, finished, err)
	}
}

func TestStreamCodeAndResultCountTowardOutputBudget(t *testing.T) {
	for _, kind := range []string{"code", "result"} {
		t.Run(kind, func(t *testing.T) {
			var value contract.Part
			var wire map[string]any
			if kind == "code" {
				text := strings.Repeat("c", (4<<20)-len("PYTHON"))
				value.ExecutableCode = &contract.ExecutableCode{Language: "PYTHON", Code: text}
				wire = map[string]any{"executableCode": map[string]any{"code": text}}
			} else {
				text := strings.Repeat("r", (4<<20)-len("OUTCOME_OK"))
				value.CodeExecutionResult = &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: text}
				wire = map[string]any{"codeExecutionResult": map[string]any{"outcome": "OUTCOME_OK", "output": text}}
			}
			parts, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", wire))
			if err != nil || !finished || len(parts) != 1 || !reflect.DeepEqual(parts[0], value) {
				t.Fatal("exact output budget rejected", finished, err)
			}
			parts, finished, err = decodeLiveFixture(t, liveFrame("IN_PROGRESS", map[string]any{"text": "x"}), liveFrame("SUCCEEDED", wire))
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || finished || len(parts) != 1 || parts[0].Text != "x" {
				t.Fatal("typed output bypassed the cumulative output budget", finished, err)
			}
		})
	}
}

func TestStreamCodeAfterOutputErrorsNeverCertifySuccess(t *testing.T) {
	first, err := json.Marshal(liveFrame("IN_PROGRESS", map[string]any{"executableCode": map[string]any{"code": "print(1)"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		`]`,
		`,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}`,
		`,{"streamAssistResponse":{"answer":{"state":"FAILED"}}}]`,
		`,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"codeExecutionResult":{"outcome":"private-status"}}}}]}}}]`,
		`,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}] {}`,
	} {
		parts, finished := 0, false
		err := decodeStream(strings.NewReader("["+string(first)+suffix), func(event contract.Event) error {
			parts += len(event.Parts)
			finished = event.Finished
			return nil
		})
		if err == nil || finished || parts != 1 {
			t.Fatal("typed output hid a later failure or truncation", parts, finished, err)
		}
	}
	consumerError := errors.New("synthetic downstream failure")
	callbacks := 0
	err = decodeStream(strings.NewReader("["+string(first)+`,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}]`), func(contract.Event) error {
		callbacks++
		return consumerError
	})
	if !errors.Is(err, consumerError) || callbacks != 1 {
		t.Fatal("code event ignored downstream failure", callbacks, err)
	}
}

func TestStreamCodeGroundingUsesDeliveredCumulativePartPositions(t *testing.T) {
	frame := func(state string, content map[string]any, source, text string) map[string]any {
		metadata := map[string]any{
			"references": []any{map[string]any{"documentMetadata": map[string]any{"uri": "https://example.test/" + source, "title": source}}},
			"segments":   []any{map[string]any{"startIndex": "0", "endIndex": len(text), "text": text, "referenceIndices": []int{0}}},
		}
		return map[string]any{"streamAssistResponse": map[string]any{"answer": map[string]any{
			"state": state, "replies": []any{map[string]any{"groundedContent": map[string]any{"content": content, "textGroundingMetadata": metadata}}},
		}}}
	}
	frames := []map[string]any{
		frame("IN_PROGRESS", map[string]any{"executableCode": map[string]any{"code": "print(1)"}}, "code", "unanchored"),
		frame("IN_PROGRESS", map[string]any{"text": "你好"}, "ordinary", "你好"),
		frame("", map[string]any{"text": "signed", "thoughtSignature": "opaque"}, "signed", "signed"),
		frame("IN_PROGRESS", map[string]any{"text": "thinking", "thought": true}, "thought", "unanchored"),
		frame("IN_PROGRESS", nil, "empty", "unanchored"),
		frame("SUCCEEDED", map[string]any{"text": "last"}, "terminal", "last"),
	}
	body, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	parts, terminalEvents := 0, 0
	var metadata *contract.GroundingMetadata
	err = decodeStream(strings.NewReader(string(body)), func(event contract.Event) error {
		parts += len(event.Parts)
		if event.GroundingMetadata != nil && (!event.Finished || len(event.Parts) != 0) {
			t.Fatal("grounding released before final successful completion")
		}
		if event.Finished {
			terminalEvents++
			metadata = event.GroundingMetadata
		}
		return nil
	})
	if err != nil || parts != 5 || terminalEvents != 1 || metadata == nil || len(metadata.GroundingChunks) != 6 || len(metadata.GroundingSupports) != 3 {
		t.Fatal("stream lost source-only metadata or invented supports", parts, terminalEvents, metadata, err)
	}
	for i, expected := range []struct {
		index, end int
		text       string
	}{{1, 6, "你好"}, {2, 6, "signed"}} {
		support := metadata.GroundingSupports[i]
		if support.Segment != (contract.GroundingSegment{PartIndex: expected.index, EndIndex: expected.end, Text: expected.text}) || !reflect.DeepEqual(support.GroundingChunkIndices, []int{i + 1}) {
			t.Fatal("support is not anchored to the delivered Part and global source", support)
		}
	}
	if support := metadata.GroundingSupports[2]; support.Segment != (contract.GroundingSegment{PartIndex: 4, EndIndex: 4, Text: "last"}) || !reflect.DeepEqual(support.GroundingChunkIndices, []int{5}) {
		t.Fatal("noncumulative terminal text was not anchored to its actual appended Part", support)
	}
	metadataEvents := 0
	err = decodeStream(strings.NewReader(string(body)+` {}`), func(event contract.Event) error {
		if event.Finished || event.GroundingMetadata != nil {
			metadataEvents++
		}
		return nil
	})
	if err == nil || metadataEvents != 0 {
		t.Fatal("trailing data released completion metadata", metadataEvents, err)
	}
}

func TestStreamCodeGroundingCountsTowardTotalOutputBudget(t *testing.T) {
	frame := liveFrame("SUCCEEDED", map[string]any{"executableCode": map[string]any{"code": strings.Repeat("c", (4<<20)-len("PYTHON"))}})
	grounded := frame["streamAssistResponse"].(map[string]any)["answer"].(map[string]any)["replies"].([]any)[0].(map[string]any)["groundedContent"].(map[string]any)
	grounded["textGroundingMetadata"] = map[string]any{"references": []any{map[string]any{"documentMetadata": map[string]any{"uri": "https://example.test/source"}}}}
	body, err := json.Marshal([]any{frame})
	if err != nil {
		t.Fatal(err)
	}
	parts, finished, metadata := 0, false, false
	err = decodeStream(strings.NewReader(string(body)), func(event contract.Event) error {
		parts += len(event.Parts)
		finished = event.Finished
		metadata = event.GroundingMetadata != nil
		return nil
	})
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || parts != 1 || finished || metadata {
		t.Fatal("grounding bypassed total payload cap or certified oversized output", parts, finished, metadata, err)
	}
}
