package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func phase6NativePrefix() []contract.Part {
	return []contract.Part{
		{Text: "pre"}, {Text: "fix"},
		{ExecutableCode: &contract.ExecutableCode{Language: "PYTHON", Code: "print(1)"}},
		{CodeExecutionResult: &contract.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: "1\n"}},
		{Text: "reason", Thought: true, ThoughtSignature: "thought-exact"},
		{Text: "proof", ThoughtSignature: "+/8="},
		{InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: []byte(`{}`)}},
		{Text: "你"}, {Text: "好"},
	}
}

func phase6CumulativeParts() []contract.Part {
	prior := phase6NativePrefix()
	return append(append([]contract.Part{{Text: "prefix"}}, prior[2:7]...), contract.Part{Text: "你好追加"})
}

func assertPhase6Projection(t *testing.T, previous, original []contract.Part, plan streamProjection) {
	t.Helper()
	output := append(append([]contract.Part(nil), previous...), plan.Parts...)
	if len(plan.Text) != len(original) {
		t.Fatal("projection omitted original Part identities")
	}
	for i, part := range original {
		covered := 0
		for _, span := range plan.Text[i] {
			if !streamGroundableText(part) || span.SourceStart != covered || span.SourceEnd <= span.SourceStart || span.SourceEnd > len(part.Text) || span.PartIndex < 0 || span.PartIndex >= len(output) {
				t.Fatal("invalid or discontinuous source mapping", i, span)
			}
			size := span.SourceEnd - span.SourceStart
			target := output[span.PartIndex]
			if !streamGroundableText(target) || span.PartStart < 0 || span.PartStart+size > len(target.Text) {
				t.Fatal("invalid destination Part mapping", i, span)
			}
			sourceText := part.Text[span.SourceStart:span.SourceEnd]
			if !utf8.ValidString(sourceText) || sourceText != target.Text[span.PartStart:span.PartStart+size] {
				t.Fatal("source projection changed bytes or split UTF8", i, span)
			}
			covered = span.SourceEnd
		}
		if streamGroundableText(part) && covered != len(part.Text) {
			t.Fatal("groundable text was not completely mapped", i, covered, len(part.Text))
		}
	}
}

func TestPhase6ProjectionCompleteNativePrefixAndExactTextMapping(t *testing.T) {
	previous, current := phase6NativePrefix(), phase6CumulativeParts()
	plan, err := projectStreamParts(previous, current, true)
	if err != nil || !plan.Matched || len(plan.Parts) != 1 || plan.Parts[0].Text != "追加" {
		t.Fatal("full native terminal prefix was not removed exactly", plan, err)
	}
	assertPhase6Projection(t, previous, current, plan)
	if !reflect.DeepEqual(plan.Text[0], []streamTextSpan{{SourceEnd: 3, PartIndex: 0}, {SourceStart: 3, SourceEnd: 6, PartIndex: 1}}) || !reflect.DeepEqual(plan.Text[6], []streamTextSpan{{SourceEnd: 3, PartIndex: 7}, {SourceStart: 3, SourceEnd: 6, PartIndex: 8}, {SourceStart: 6, SourceEnd: 12, PartIndex: 9}}) {
		t.Fatal("resegmented terminal text did not map to old and new Part intervals", plan.Text)
	}
	if len(plan.Text[3]) != 0 || !reflect.DeepEqual(plan.Text[4], []streamTextSpan{{SourceEnd: 5, PartIndex: 5}}) {
		t.Fatal("thought was anchored or exact signed text lost its boundary")
	}
	// Terminal resegmentation can also split one earlier text Part. The second
	// source fragment must start in the middle of that same delivered Part.
	previous = []contract.Part{{Text: "你好"}}
	current = []contract.Part{{Text: "你"}, {Text: "好世"}, {Text: "界"}}
	plan, err = projectStreamParts(previous, current, true)
	if err != nil || !plan.Matched || len(plan.Parts) != 2 || plan.Parts[0].Text != "世" || plan.Parts[1].Text != "界" || plan.Text[1][0].PartStart != 3 {
		t.Fatal("split terminal Part mapping lost the old target offset", plan, err)
	}
	assertPhase6Projection(t, previous, current, plan)
}

func TestPhase6ProjectionMismatchNeverDeletesPartialOrTypedPrefix(t *testing.T) {
	for name, mutate := range map[string]func([]contract.Part) []contract.Part{
		"code":                  func(p []contract.Part) []contract.Part { p[1].ExecutableCode.Code = "print(2)"; return p },
		"result":                func(p []contract.Part) []contract.Part { p[2].CodeExecutionResult.Output = "2\n"; return p },
		"signature-wire":        func(p []contract.Part) []contract.Part { p[4].ThoughtSignature = "-_8"; return p },
		"thought":               func(p []contract.Part) []contract.Part { p[3].Thought = false; return p },
		"missing-blob":          func(p []contract.Part) []contract.Part { return append(p[:5], p[6:]...) },
		"reordered-typed":       func(p []contract.Part) []contract.Part { p[1], p[2] = p[2], p[1]; return p },
		"ordinary-crosses-code": func(p []contract.Part) []contract.Part { p[0].Text = "prefix你好"; p[6].Text = "追加"; return p },
		"incomplete":            func(p []contract.Part) []contract.Part { return p[:5] },
		"nonprefix":             func(p []contract.Part) []contract.Part { return append([]contract.Part{{Text: "unrelated"}}, p...) },
	} {
		t.Run(name, func(t *testing.T) {
			previous := phase6NativePrefix()
			current := mutate(phase6CumulativeParts())
			plan, err := projectStreamParts(previous, current, true)
			if err != nil || plan.Matched || !reflect.DeepEqual(plan.Parts, current) {
				t.Fatal("partial match removed native terminal data", plan, err)
			}
			for _, spans := range plan.Text {
				for _, span := range spans {
					if span.PartIndex < len(previous) {
						t.Fatal("failed partial match leaked a tentative old-text anchor")
					}
				}
			}
			assertPhase6Projection(t, previous, current, plan)
		})
	}
}

func TestPhase6ProjectionNonterminalEmptyAndPartLimits(t *testing.T) {
	previous := phase6NativePrefix()
	current := phase6CumulativeParts()
	plan, err := projectStreamParts(previous, current, false)
	if err != nil || plan.Matched || !reflect.DeepEqual(plan.Parts, current) {
		t.Fatal("in-progress repetition was mistaken for a terminal snapshot", err)
	}
	assertPhase6Projection(t, previous, current, plan)
	plan, err = projectStreamParts([]contract.Part{{Text: ""}}, []contract.Part{{Text: ""}}, true)
	if err != nil || plan.Matched || len(plan.Parts) != 1 {
		t.Fatal("empty ordinary text alone was treated as snapshot evidence", plan, err)
	}
	for _, count := range []int{4095, 4096} {
		previous := make([]contract.Part, count)
		for i := range previous {
			previous[i].Text = "x"
		}
		current := []contract.Part{{Text: strings.Repeat("x", count) + "y"}}
		plan, err := projectStreamParts(previous, current, true)
		if count == 4095 {
			if err != nil || len(plan.Parts) != 1 || plan.Parts[0].Text != "y" {
				t.Fatal("exact cumulative 4096-Part budget rejected", err)
			}
		} else {
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" {
				t.Fatal("new suffix bypassed cumulative Part budget", err)
			}
		}
	}
	if _, err := projectStreamParts(nil, make([]contract.Part, 4097), true); err == nil {
		t.Fatal("unbounded terminal Part descriptors accepted")
	}
}

func phase6WireParts(t *testing.T, parts []contract.Part) []map[string]any {
	t.Helper()
	var values []map[string]any
	for _, part := range parts {
		data, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if part.ExecutableCode != nil {
			value["executableCode"] = map[string]any{"code": part.ExecutableCode.Code}
		}
		values = append(values, value)
	}
	return values
}

func TestPhase6StreamNativeCumulativeOutputRetainsOneExactSequence(t *testing.T) {
	previous, current := phase6NativePrefix(), phase6CumulativeParts()
	parts, finished, err := decodeLiveFixture(t, liveFrame("IN_PROGRESS", phase6WireParts(t, previous)...), liveFrame("SUCCEEDED", phase6WireParts(t, current)...))
	want := append(previous, contract.Part{Text: "追加"})
	if err != nil || !finished || !reflect.DeepEqual(parts, want) {
		t.Fatal("full native cumulative stream lost or duplicated Parts", parts, finished, err)
	}
}

func TestPhase6StreamTerminalGroundingSpansOldAndNewParts(t *testing.T) {
	first := liveFrame("IN_PROGRESS", map[string]any{"text": "你"}, map[string]any{"text": "好"})
	last := liveFrame("SUCCEEDED", map[string]any{"text": "你好世界"})
	grounded := last["streamAssistResponse"].(map[string]any)["answer"].(map[string]any)["replies"].([]any)[0].(map[string]any)["groundedContent"].(map[string]any)
	grounded["textGroundingMetadata"] = map[string]any{
		"references": []any{map[string]any{"documentMetadata": map[string]any{"uri": "https://example.test/terminal-source"}}},
		"segments":   []any{map[string]any{"startIndex": "0", "endIndex": "12", "text": "你好世界", "referenceIndices": []int{0}}},
	}
	body, err := json.Marshal([]any{first, last})
	if err != nil {
		t.Fatal(err)
	}
	var parts []contract.Part
	var metadata *contract.GroundingMetadata
	finished := 0
	err = decodeStream(strings.NewReader(string(body)), func(event contract.Event) error {
		parts = append(parts, event.Parts...)
		if event.Finished {
			finished++
			metadata = event.GroundingMetadata
		}
		return nil
	})
	if err != nil || finished != 1 || len(parts) != 3 || parts[2].Text != "世界" || metadata == nil || len(metadata.GroundingChunks) != 1 || len(metadata.GroundingSupports) != 3 {
		t.Fatal("cumulative grounding was lost or attached to guessed text", parts, metadata, err)
	}
	for i, support := range metadata.GroundingSupports {
		if support.Segment != (contract.GroundingSegment{PartIndex: i, EndIndex: len(parts[i].Text), Text: parts[i].Text}) || !reflect.DeepEqual(support.GroundingChunkIndices, []int{0}) {
			t.Fatal("cumulative support does not describe the delivered Part interval", support)
		}
	}
	for _, malformed := range []string{strings.TrimSuffix(string(body), "]"), string(body) + `{}`} {
		terminal := false
		err := decodeStream(strings.NewReader(malformed), func(event contract.Event) error {
			terminal = terminal || event.Finished || event.GroundingMetadata != nil || event.CitationMetadata != nil
			return nil
		})
		if err == nil || terminal {
			t.Fatal("projected terminal metadata bypassed complete-array/EOF requirement", terminal, err)
		}
	}
}
