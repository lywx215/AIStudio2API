package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func TestStreamDiagnosticsExposeOnlyFixedReasonsThroughOperationError(t *testing.T) {
	part := func(value string) string {
		return `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":` + value + `}}]}}}]`
	}
	for _, test := range []struct {
		body   string
		reason streamDiagnostic
	}{
		{part(`{"text":"private-value","thought":null}`), streamInvalidThought},
		{part(`{"text":"private-value","thoughtSignature":null}`), streamInvalidSignature},
		{part(`{"text":"private-value","text":"other-value"}`), streamDuplicateJSONKey},
		{part(`{"text":"` + string([]byte{0xff}) + `"}`), streamInvalidUTF8},
		{part(`{"text":"\ud800"}`), streamInvalidUnicodeEscape},
		{part(`{"private-field":"private-value"}`), streamUnsupportedContent},
		{`[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS"}}}]`, streamMissingTerminalSuccess},
		{strings.TrimSuffix(part(`{"text":"private-value"}`), "]"), streamTruncatedJSON},
		{part(`{"text":"private-value"}`) + `{}`, streamTrailingData},
		{strings.TrimSuffix(part(`{"text":"private-value"}`), "]") + `,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}]`, streamDataAfterTerminal},
		{`[{"unused":` + strings.Repeat("[", 65) + `null` + strings.Repeat("]", 65) + `}]`, streamNestingLimit},
		{`[{"streamAssistResponse":???}]`, streamInvalidJSON},
	} {
		t.Run(string(test.reason), func(t *testing.T) {
			finished := false
			err := decodeStream(strings.NewReader(test.body), func(event contract.Event) error { finished = event.Finished; return nil })
			if err == nil || finished {
				t.Fatal("invalid stream became successful")
			}
			classified := classifyOperation(err, StageStream, true, true, context.Background())
			var failure *contract.Failure
			want := "Business response is malformed or incomplete (" + string(test.reason) + ")"
			if !errors.As(classified, &failure) || failure.Message != want || failure.Code != 502 || classified.Kind != ErrorProtocol {
				t.Fatal("fixed diagnostic was lost or private upstream data exposed", classified)
			}
		})
	}
}

func TestStreamMetadataRejectsLossyValuesAndDuplicateKeys(t *testing.T) {
	frame := `{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":CONTENT}}]}}}`
	for _, content := range []string{
		`{"text":"x","thought":null}`,
		`{"text":"x","thoughtSignature":null}`,
		`{"text":"x","thought":false,"thought":true}`,
		`{"text":"x","thoughtSignature":"a","thoughtSignature":"b"}`,
		`{"text":"first","\u0074ext":"second"}`,
		`{"text":"` + string([]byte{0xff}) + `"}`,
		`{"text":"\ud800"}`,
		`{"text":"\udfff"}`,
		`{"text":"x","thoughtSignature":"\ud800x"}`,
	} {
		t.Run(content, func(t *testing.T) {
			finished := false
			body := "[" + strings.Replace(frame, "CONTENT", content, 1) + "]"
			err := decodeStream(strings.NewReader(body), func(event contract.Event) error { finished = event.Finished; return nil })
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "DATA_LOSS" || finished {
				t.Fatalf("lossy response accepted: finished=%v err=%v", finished, err)
			}
		})
	}
	for _, body := range []string{
		`[{"streamAssistResponse":{"answer":{"state":"FAILED","state":"SUCCEEDED"}}}]`,
		`[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}},"streamAssistResponse":{}}]`,
		`[null,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}]`,
	} {
		finished := false
		err := decodeStream(strings.NewReader(body), func(event contract.Event) error { finished = event.Finished; return nil })
		if err == nil || finished {
			t.Fatal("ambiguous terminal envelope accepted", finished, err)
		}
	}
}

func TestStreamMetadataOnlyRepliesNeedSeparateSuccessfulCompletion(t *testing.T) {
	first := `{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"text":"visible "}}}]}}}`
	last := `{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"visible suffix"}}}]}}}`
	unknown := `{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"unknown":"private-value"}}}]}}}`
	// The live diagnostic proves empty content, without distinguishing these
	// three empty representations. None contains a native content Part.
	for _, shape := range []struct{ name, field string }{
		{"missing", ""}, {"null", `"content":null`}, {"empty-object", `"content":{}`},
	} {
		metadata := `{"streamAssistResponse":{"answer":{"replies":[{"groundedContent":{` + shape.field + `}}]}}}`
		t.Run(shape.name, func(t *testing.T) {
			var output []contract.Part
			finished := 0
			err := decodeStream(strings.NewReader("["+first+","+metadata+","+last+"]"), func(event contract.Event) error {
				output = append(output, event.Parts...)
				if event.Finished {
					finished++
				}
				return nil
			})
			if err != nil || finished != 1 || len(output) != 2 || output[0].Text != "visible " || output[1].Text != "suffix" {
				t.Fatal("empty reply changed text, added a Part or lost completion", output, finished, err)
			}
			for _, bad := range []struct {
				body   string
				reason streamDiagnostic
			}{
				{"[" + metadata + "]", streamMissingTerminalSuccess},
				{"[" + first + "," + metadata + "]", streamMissingTerminalSuccess},
				{"[" + first + "," + metadata + "," + last, streamTruncatedJSON},
				{"[" + first + "," + metadata + "," + unknown + "]", streamUnsupportedContent},
			} {
				finished := false
				err := decodeStream(strings.NewReader(bad.body), func(event contract.Event) error { finished = event.Finished; return nil })
				if err == nil || finished || !strings.Contains(err.Error(), "("+string(bad.reason)+")") {
					t.Fatal("metadata-only reply masked an incomplete or unsupported stream", finished, err)
				}
			}
		})
	}
	duplicated := `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":null,"content":{}}}]}}}]`
	if err := decodeStream(strings.NewReader(duplicated), func(contract.Event) error { return nil }); err == nil || !strings.Contains(err.Error(), string(streamDuplicateJSONKey)) {
		t.Fatal("empty metadata bypassed duplicate key validation", err)
	}
}

func TestStreamUnicodeEscapesDoNotRewriteValidText(t *testing.T) {
	body := `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"\ud83d\ude00 \\ud800 \"quoted\" \u4f60\u597d"}}}]}}}]`
	var text string
	err := decodeStream(strings.NewReader(body), func(event contract.Event) error {
		for _, part := range event.Parts {
			text += part.Text
		}
		return nil
	})
	if err != nil || text != "😀 \\ud800 \"quoted\" 你好" {
		t.Fatal("valid escapes changed", text, err)
	}
}

func TestStreamEmptySignedPartKeepsExactBoundary(t *testing.T) {
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "visible"}),
		liveFrame("SUCCEEDED", map[string]any{"text": "", "thought": false, "thoughtSignature": "opaque-exact+/="}),
	)
	if err != nil || !finished || len(parts) != 2 || parts[0].Text != "visible" || parts[1].Text != "" || parts[1].Thought || parts[1].ThoughtSignature != "opaque-exact+/=" {
		t.Fatal("empty signed part lost or combined", parts, finished, err)
	}
}

func TestStreamByteLimitIncludesWhitespaceAndCannotInventEOF(t *testing.T) {
	base := `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED"}}}]`
	for _, test := range []struct {
		name, body string
		wantOK     bool
	}{
		{"exact-limit", base + strings.Repeat(" ", maxStreamBytes-len(base)), true},
		{"one-byte-over", base + strings.Repeat(" ", maxStreamBytes-len(base)+1), false},
		{"hidden-data-after-limit", base + strings.Repeat(" ", maxStreamBytes-len(base)) + `{}`, false},
		{"leading-whitespace-counted", strings.Repeat(" ", maxStreamBytes-len(base)+1) + base, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			finished := false
			err := decodeStream(strings.NewReader(test.body), func(event contract.Event) error { finished = event.Finished; return nil })
			if test.wantOK {
				if err != nil || !finished {
					t.Fatal("valid boundary rejected", finished, err)
				}
				return
			}
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || finished {
				t.Fatal("oversized response accepted", finished, err)
			}
		})
	}
}
