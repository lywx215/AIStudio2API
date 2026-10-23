package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

// The role:model shape was observed in the existing sanitized Argon response
// artifact. These fixtures contain only synthetic text; they make no live call.
func liveFrame(state string, parts ...map[string]any) map[string]any {
	replies := []any{}
	for _, part := range parts {
		replies = append(replies, map[string]any{"groundedContent": map[string]any{"content": part}})
	}
	return map[string]any{"streamAssistResponse": map[string]any{"answer": map[string]any{"state": state, "replies": replies}}}
}

func decodeLiveFixture(t *testing.T, frames ...map[string]any) ([]contract.Part, bool, error) {
	t.Helper()
	raw, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	var parts []contract.Part
	finished := false
	err = decodeStream(strings.NewReader(string(raw)), func(event contract.Event) error {
		parts = append(parts, event.Parts...)
		finished = event.Finished
		return nil
	})
	return parts, finished, err
}

func TestLiveStreamRoleModelCompatibility(t *testing.T) {
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"role": "model", "text": "synthetic "}),
		liveFrame("SUCCEEDED", map[string]any{"role": "model", "text": "answer"}),
	)
	if err != nil || !finished || len(parts) != 2 || parts[0].Text+parts[1].Text != "synthetic answer" {
		t.Fatal(parts, finished, err)
	}
	for _, role := range []any{"user", "assistant", "", nil, 1} {
		_, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", map[string]any{"role": role, "text": "x"}))
		if err == nil || finished {
			t.Fatal("unexpected role accepted", role)
		}
	}
}

func TestLiveStreamTerminalCumulativePrefixDelta(t *testing.T) {
	for _, terminal := range [][]map[string]any{
		{{"role": "model", "text": "prefix suffix"}},
		{{"role": "model", "text": "pre"}, {"role": "model", "text": "fix suffix"}},
	} {
		parts, finished, err := decodeLiveFixture(t,
			liveFrame("IN_PROGRESS", map[string]any{"role": "model", "text": "prefix "}),
			liveFrame("SUCCEEDED", terminal...),
		)
		var text strings.Builder
		for _, part := range parts {
			text.WriteString(part.Text)
		}
		if err != nil || !finished || text.String() != "prefix suffix" {
			t.Fatal(text.String(), finished, err)
		}
	}
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "all"}),
		liveFrame("SUCCEEDED", map[string]any{"text": "all"}),
	)
	if err != nil || !finished || len(parts) != 1 || parts[0].Text != "all" {
		t.Fatal("exact terminal repeat should not duplicate", parts, finished, err)
	}
	parts, finished, err = decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "prefix"}),
		liveFrame("SUCCEEDED", map[string]any{"text": "unrelated delta"}),
	)
	if err != nil || !finished || len(parts) != 2 || parts[1].Text != "unrelated delta" {
		t.Fatal("nonprefix terminal text changed", parts, finished, err)
	}
}

func TestLiveStreamThoughtAndSignatureBoundariesStayOpaque(t *testing.T) {
	for _, special := range []map[string]any{
		{"role": "model", "text": "prefix suffix", "thought": true},
		{"role": "model", "text": "prefix suffix", "thoughtSignature": "opaque-exact"},
	} {
		parts, finished, err := decodeLiveFixture(t,
			liveFrame("IN_PROGRESS", map[string]any{"role": "model", "text": "prefix "}),
			liveFrame("SUCCEEDED", special),
		)
		if err != nil || !finished || len(parts) != 2 || parts[1].Text != "prefix suffix" || (parts[1].ThoughtSignature == "" && !parts[1].Thought) {
			t.Fatal("signed/thought terminal part was merged", parts, finished, err)
		}
	}
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "reason", "thought": true}, map[string]any{"text": "prefix "}),
		liveFrame("SUCCEEDED", map[string]any{"text": "prefix suffix"}),
	)
	if err != nil || !finished || len(parts) != 3 || parts[2].Text != "prefix suffix" || !parts[0].Thought {
		t.Fatal("terminal missing the thought prefix was guessed into a cumulative snapshot", parts, finished, err)
	}
}

func TestLiveStreamUnknownMediaStillRejects(t *testing.T) {
	for _, part := range []map[string]any{
		{"role": "model", "inlineData": map[string]any{"mimeType": "image/svg+xml", "data": "eA=="}},
		{"role": "model", "file": map[string]any{"fileId": "synthetic-id"}},
		{"role": "model", "functionCall": map[string]any{"name": "synthetic"}},
	} {
		_, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", part))
		if err == nil || finished {
			t.Fatal("unknown media part silently discarded", part)
		}
	}
}

func TestLiveStreamSuggestionsRetainNativeInlineData(t *testing.T) {
	payload := []byte(`{"suggestions":["synthetic"],"n":9007199254740993,"enabled":false}`)
	blob := map[string]any{"role": "model", "inlineData": map[string]any{"mimeType": contract.SuggestionsMIMEType, "data": base64.StdEncoding.EncodeToString(payload)}}
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", map[string]any{"text": "prefix "}),
		liveFrame("SUCCEEDED", blob, map[string]any{"text": "prefix suffix"}),
	)
	if err != nil || !finished || len(parts) != 3 || parts[1].InlineData == nil || string(parts[1].InlineData.Data) != string(payload) || parts[1].InlineData.MIMEType != contract.SuggestionsMIMEType || parts[1].Text != "" || parts[2].Text != "prefix suffix" {
		t.Fatal(parts, finished, err)
	}
	wire, err := json.Marshal(parts[1])
	if err != nil || strings.Contains(string(wire), `"text"`) || !strings.Contains(string(wire), `"mimeType":"application/json+suggestions"`) || !strings.Contains(string(wire), base64.StdEncoding.EncodeToString(payload)) {
		t.Fatal("native inline part changed", string(wire), err)
	}
}

func TestLiveStreamSuggestionsStrictBase64JSONAndLimits(t *testing.T) {
	for _, encoded := range []string{
		`{"suggestions":[]}`, "e31=", "e30=\n", "bm90LWpzb24=", "/w==", "", "bnVsbA==garbage",
	} {
		_, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", map[string]any{"inlineData": map[string]any{"mimeType": contract.SuggestionsMIMEType, "data": encoded}}))
		if err == nil || finished {
			t.Fatal("invalid suggestions accepted", encoded)
		}
	}
	for _, raw := range []string{
		`{"mimeType":"application/json+suggestions","data":"e30=","data":"e30="}`,
		`{"mimeType":"application/json+suggestions","data":"e30=","unknown":1}`,
		`{"mimeType":"application/json+suggestions","data":null}`,
	} {
		if _, err := decodeInlineData(json.RawMessage(raw)); err == nil {
			t.Fatal("malformed Blob accepted", raw)
		}
	}
	_, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", map[string]any{"text": "mixed", "inlineData": map[string]any{"mimeType": contract.SuggestionsMIMEType, "data": "e30="}}))
	if err == nil || finished {
		t.Fatal("mixed text/inlineData union accepted")
	}
	largeJSON := []byte(`"` + strings.Repeat("x", 2<<20) + `"`)
	_, err = decodeInlineData(json.RawMessage(`{"mimeType":"application/json+suggestions","data":"` + base64.StdEncoding.EncodeToString(largeJSON) + `"}`))
	if err == nil {
		t.Fatal("decoded and encoded byte budget was bypassed")
	}
}
