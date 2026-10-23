package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func lateGroundingReply(id string, content map[string]any, metadata any) map[string]any {
	grounded := map[string]any{}
	if content != nil {
		grounded["content"] = content
	}
	if metadata != nil {
		grounded["textGroundingMetadata"] = metadata
	}
	reply := map[string]any{"groundedContent": grounded}
	if id != "" {
		reply["replyId"] = id
	}
	return reply
}

func lateGroundingFrame(state string, replies ...map[string]any) map[string]any {
	return map[string]any{"streamAssistResponse": map[string]any{"answer": map[string]any{"state": state, "replies": replies}}}
}

func lateGroundingMetadata(t *testing.T, segments ...any) json.RawMessage {
	return groundingFixture(t, []any{groundingReference("https://example.test/source", "Source")}, segments)
}

func decodeLateGrounding(t *testing.T, frames ...map[string]any) ([]contract.Part, *contract.GroundingMetadata, error) {
	t.Helper()
	raw, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	var parts []contract.Part
	var metadata *contract.GroundingMetadata
	err = decodeStream(strings.NewReader(string(raw)), func(event contract.Event) error {
		parts = append(parts, event.Parts...)
		if event.GroundingMetadata != nil && !event.Finished {
			t.Fatal("late grounding escaped before successful EOF")
		}
		if event.Finished {
			metadata = event.GroundingMetadata
		}
		return nil
	})
	return parts, metadata, err
}

func TestReplyGroundingSameIDAfterSuggestions(t *testing.T) {
	metadata := lateGroundingMetadata(t, groundingSegment(0, 3, "甲", 0), groundingSegment(3, 7, "🙂", 0), groundingSegment(7, 12, "Omega", 0), groundingSegment(0, 12, "甲🙂Omega", 0))
	text := lateGroundingReply("reply-a", map[string]any{"text": "甲🙂Omega"}, nil)
	inline := lateGroundingReply("reply-a", map[string]any{"inlineData": map[string]any{"mimeType": contract.SuggestionsMIMEType, "data": "e30="}}, nil)
	late := lateGroundingReply("reply-a", nil, metadata)
	for _, sameFrame := range []bool{false, true} {
		t.Run(fmt.Sprint(sameFrame), func(t *testing.T) {
			frames := []map[string]any{lateGroundingFrame("IN_PROGRESS", text), lateGroundingFrame("IN_PROGRESS", inline), lateGroundingFrame("SUCCEEDED", late)}
			if sameFrame {
				frames = []map[string]any{lateGroundingFrame("SUCCEEDED", text, inline, late)}
			}
			parts, got, err := decodeLateGrounding(t, frames...)
			if err != nil || len(parts) != 2 || got == nil || len(got.GroundingChunks) != 1 || len(got.GroundingSupports) != 4 {
				t.Fatal("same-ID delayed supports lost", len(parts), got, err)
			}
			for _, support := range got.GroundingSupports {
				segment := support.Segment
				if segment.PartIndex != 0 || parts[0].Text[segment.StartIndex:segment.EndIndex] != segment.Text {
					t.Fatal("support did not identify delivered UTF-8 text", segment)
				}
			}
		})
	}
}

func TestReplyGroundingCumulativePrefixDoesNotAppendAgain(t *testing.T) {
	metadata := lateGroundingMetadata(t, groundingSegment(0, 8, "甲🙂Z", 0))
	parts, got, err := decodeLateGrounding(t,
		lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", map[string]any{"text": "甲"}, nil)),
		lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", map[string]any{"text": "🙂"}, nil)),
		lateGroundingFrame("SUCCEEDED", lateGroundingReply("a", map[string]any{"text": "甲🙂Z"}, nil), lateGroundingReply("a", nil, metadata)),
	)
	if err != nil || len(parts) != 3 || got == nil || len(got.GroundingSupports) != 3 {
		t.Fatal("cumulative anchor was duplicated or lost", parts, got, err)
	}
	for i, support := range got.GroundingSupports {
		if support.Segment.PartIndex != i || support.Segment.StartIndex != 0 || support.Segment.Text != parts[i].Text {
			t.Fatal("cumulative anchor does not match actual global Parts", support)
		}
	}
}

func TestReplyGroundingDoesNotGuessIdentityOrOffsets(t *testing.T) {
	for _, tc := range []struct {
		name, textID, metadataID string
		segment                  any
		before, switchID         bool
	}{
		{"missing-id", "", "", groundingSegment(0, 3, "甲", 0), false, false},
		{"different-id", "a", "b", groundingSegment(0, 3, "甲", 0), false, false},
		{"terminal-id-switch", "a", "b", groundingSegment(0, 3, "甲", 0), false, true},
		{"missing-text", "a", "a", map[string]any{"startIndex": 0, "endIndex": 3, "referenceIndices": []int{0}}, false, false},
		{"empty-text", "a", "a", groundingSegment(0, 3, "", 0), false, false},
		{"mismatch", "a", "a", groundingSegment(0, 3, "乙", 0), false, false},
		{"middle-rune", "a", "a", groundingSegment(1, 3, "甲", 0), false, false},
		{"outside-text", "a", "a", groundingSegment(0, 9, "甲", 0), false, false},
		{"before-text", "a", "a", groundingSegment(0, 3, "甲", 0), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := lateGroundingReply(tc.textID, map[string]any{"text": "甲"}, nil)
			late := lateGroundingReply(tc.metadataID, nil, lateGroundingMetadata(t, tc.segment))
			frames := []map[string]any{lateGroundingFrame("IN_PROGRESS", text), lateGroundingFrame("SUCCEEDED", late)}
			if tc.before {
				frames = []map[string]any{lateGroundingFrame("SUCCEEDED", late, text)}
			}
			if tc.switchID {
				frames[1] = lateGroundingFrame("SUCCEEDED", lateGroundingReply("b", map[string]any{"text": "甲"}, nil), late)
			}
			_, got, err := decodeLateGrounding(t, frames...)
			if err != nil || got == nil || len(got.GroundingChunks) != 1 || len(got.GroundingSupports) != 0 {
				t.Fatal("unproven late metadata acquired a support", got, err)
			}
		})
	}
}

func TestReplyGroundingLaterTypedBoundaryInvalidatesPendingAnchor(t *testing.T) {
	for name, content := range map[string]map[string]any{
		"signed":        {"text": "signed", "thoughtSignature": "opaque"},
		"code":          {"executableCode": map[string]any{"code": "print(1)"}},
		"result":        {"codeExecutionResult": map[string]any{"outcome": "OUTCOME_OK", "output": "1"}},
		"image":         {"inlineData": map[string]any{"mimeType": "image/png", "data": "AA=="}},
		"thought-image": {"inlineData": map[string]any{"mimeType": "image/png", "data": "AA=="}, "thought": true},
	} {
		t.Run(name, func(t *testing.T) {
			_, got, err := decodeLateGrounding(t,
				lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", map[string]any{"text": "a"}, nil), lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(0, 1, "a", 0)))),
				lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", content, nil)),
				lateGroundingFrame("SUCCEEDED", lateGroundingReply("a", map[string]any{"text": "b"}, nil), lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(0, 2, "ab", 0)))),
			)
			if err != nil || got == nil || len(got.GroundingSupports) != 0 {
				t.Fatal("pending support survived an unproven typed boundary", got, err)
			}
		})
	}
}

func TestReplyGroundingThoughtTextIsExcludedFromVisibleOffsets(t *testing.T) {
	for _, position := range []string{"before-text", "after-text", "after-metadata"} {
		t.Run(position, func(t *testing.T) {
			thought := lateGroundingReply("a", map[string]any{"text": "private thought", "thought": true, "thoughtSignature": "opaque-exact+/="}, nil)
			text := lateGroundingReply("a", map[string]any{"text": "甲🙂"}, nil)
			late := lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(3, 7, "🙂", 0)))
			replies, index := []map[string]any{thought, text, late}, 1
			if position == "after-text" {
				replies, index = []map[string]any{text, thought, late}, 0
			} else if position == "after-metadata" {
				replies, index = []map[string]any{text, late, thought}, 0
			}
			frames := make([]map[string]any, 0, len(replies))
			for i, reply := range replies {
				state := "IN_PROGRESS"
				if i == len(replies)-1 {
					state = "SUCCEEDED"
				}
				frames = append(frames, lateGroundingFrame(state, reply))
			}
			parts, got, err := decodeLateGrounding(t, frames...)
			if err != nil || len(parts) != 2 || got == nil || len(got.GroundingSupports) != 1 {
				t.Fatal("thought text incorrectly invalidated visible grounding", parts, got, err)
			}
			want := contract.GroundingSegment{PartIndex: index, StartIndex: 3, EndIndex: 7, Text: "🙂"}
			if got.GroundingSupports[0].Segment != want {
				t.Fatal("visible offset included thought bytes or lost global Part index", got.GroundingSupports[0].Segment)
			}
			if thoughtPart := parts[1-index]; !thoughtPart.Thought || thoughtPart.Text != "private thought" || thoughtPart.ThoughtSignature != "opaque-exact+/=" {
				t.Fatal("thought or signature was changed", thoughtPart)
			}
		})
	}
}

func TestReplyGroundingCannotAnchorToThoughtText(t *testing.T) {
	_, got, err := decodeLateGrounding(t,
		lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", map[string]any{"text": "secret", "thought": true}, nil)),
		lateGroundingFrame("SUCCEEDED", lateGroundingReply("a", map[string]any{"text": "public"}, nil), lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(0, 6, "secret", 0)))),
	)
	if err != nil || got == nil || len(got.GroundingSupports) != 0 {
		t.Fatal("a segment matching only thought text acquired a support", got, err)
	}
}

func TestReplyGroundingMetadataPrefixCannotBorrowLaterText(t *testing.T) {
	_, got, err := decodeLateGrounding(t,
		lateGroundingFrame("IN_PROGRESS", lateGroundingReply("a", map[string]any{"text": "a"}, nil), lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(0, 2, "ab", 0)))),
		lateGroundingFrame("SUCCEEDED", lateGroundingReply("a", map[string]any{"text": "b"}, nil)),
	)
	if err != nil || got == nil || len(got.GroundingSupports) != 0 {
		t.Fatal("metadata borrowed a later same-ID text fragment", got, err)
	}
}

func TestReplyGroundingFailureNeverReleasesMetadata(t *testing.T) {
	frames := []map[string]any{lateGroundingFrame("SUCCEEDED", lateGroundingReply("a", map[string]any{"text": "a"}, nil), lateGroundingReply("a", nil, lateGroundingMetadata(t, groundingSegment(0, 1, "a", 0))))}
	raw, _ := json.Marshal(frames)
	for _, suffix := range []string{" trailing", ""} {
		emits, finished := 0, false
		consumerErr := errors.New("synthetic delivery failure")
		err := decodeStream(strings.NewReader(string(raw)+suffix), func(event contract.Event) error {
			emits++
			finished = finished || event.Finished || event.GroundingMetadata != nil
			if suffix == "" {
				return consumerErr
			}
			return nil
		})
		if err == nil || finished || emits != 1 {
			t.Fatal("failed stream certified late grounding", emits, finished, err)
		}
	}
}

func TestReplyGroundingIdentityAndResourceBounds(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		for _, raw := range []string{`null`, `1`, `{}`, `"` + strings.Repeat("x", maxGroundingReplyIDBytes+1) + `"`} {
			if _, err := decodeGroundingReplyID(json.RawMessage(raw)); err == nil {
				t.Fatal("invalid or oversized ID accepted")
			}
		}
		if _, err := decodeGroundingReplyID(json.RawMessage(`"` + strings.Repeat("x", maxGroundingReplyIDBytes) + `"`)); err != nil {
			t.Fatal("exact ID limit rejected", err)
		}
	})
	t.Run("count", func(t *testing.T) {
		var index replyGroundingIndex
		for i := 0; i < maxGroundingReplyIDs; i++ {
			if _, err := index.anchor(fmt.Sprint(i)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := index.anchor("overflow"); err == nil {
			t.Fatal("ID count unbounded")
		}
	})
	t.Run("id-bytes", func(t *testing.T) {
		var index replyGroundingIndex
		for i := 0; i < maxGroundingReplyIDTotal/maxGroundingReplyIDBytes; i++ {
			if _, err := index.anchor(fmt.Sprintf("%04d", i) + strings.Repeat("x", maxGroundingReplyIDBytes-4)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := index.anchor("overflow"); err == nil {
			t.Fatal("aggregate ID bytes unbounded")
		}
	})
	t.Run("assembly-work", func(t *testing.T) {
		replies := []map[string]any{lateGroundingReply("a", map[string]any{"text": "A" + strings.Repeat("x", (2<<20)-1)}, nil)}
		metadata := lateGroundingMetadata(t, groundingSegment(0, 1, "A", 0))
		for range 9 {
			replies = append(replies, lateGroundingReply("a", nil, metadata))
		}
		_, got, err := decodeLateGrounding(t, lateGroundingFrame("SUCCEEDED", replies...))
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || got != nil {
			t.Fatal("repeated late metadata bypassed assembly work budget", got, err)
		}
	})
}
