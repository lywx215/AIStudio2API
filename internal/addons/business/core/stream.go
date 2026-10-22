package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

// decodeStream consumes one JSON envelope at a time, keeping downstream
// backpressure. Closing the array without a terminal answer is truncation.
func decodeStream(r io.Reader, emit func(contract.Event) error) error {
	decoder := json.NewDecoder(io.LimitReader(r, (8<<20)+1))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return malformedStream()
	}
	finished := false
	outputBytes := 0
	var ordinaryText strings.Builder
	for decoder.More() {
		var frame struct {
			Error    json.RawMessage `json:"error"`
			Response struct {
				Answer struct {
					State   string `json:"state"`
					Replies []struct {
						Grounded *struct {
							Content map[string]json.RawMessage `json:"content"`
						} `json:"groundedContent"`
					} `json:"replies"`
				} `json:"answer"`
			} `json:"streamAssistResponse"`
		}
		if decoder.Decode(&frame) != nil || decoder.InputOffset() > 8<<20 {
			return malformedStream()
		}
		if len(frame.Error) > 0 {
			return contract.Fail(502, "UNAVAILABLE", "Business returned an upstream error")
		}
		answer := frame.Response.Answer
		if finished && (answer.State != "" || len(answer.Replies) > 0) {
			return malformedStream()
		}
		parts := []contract.Part{}
		for _, reply := range answer.Replies {
			if reply.Grounded == nil {
				return contract.Fail(502, "UNIMPLEMENTED", "Business returned an unsupported reply")
			}
			c := reply.Grounded.Content
			for field := range c {
				if field != "text" && field != "thought" && field != "thoughtSignature" && field != "role" && field != "inlineData" {
					return contract.Fail(502, "UNIMPLEMENTED", "Business returned an unsupported content part")
				}
			}
			if raw, ok := c["role"]; ok {
				var role string
				if json.Unmarshal(raw, &role) != nil || role != "model" {
					return malformedStream()
				}
			}
			if raw, ok := c["inlineData"]; ok {
				// The observed auxiliary suggestions part has no text/thought or
				// signature sibling. Other mixed unions remain unsupported.
				for field := range c {
					if field != "inlineData" && field != "role" {
						return malformedStream()
					}
				}
				blob, err := decodeSuggestions(raw)
				if err != nil {
					return err
				}
				parts = append(parts, contract.Part{InlineData: blob})
			} else if raw, ok := c["text"]; ok {
				var part contract.Part
				if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &part.Text) != nil {
					return malformedStream()
				}
				if raw, ok := c["thought"]; ok && json.Unmarshal(raw, &part.Thought) != nil {
					return malformedStream()
				}
				if raw, ok := c["thoughtSignature"]; ok && json.Unmarshal(raw, &part.ThoughtSignature) != nil {
					return malformedStream()
				}
				parts = append(parts, part)
			} else if len(c) > 0 {
				return malformedStream()
			}
		}
		switch answer.State {
		case "SUCCEEDED":
			parts = terminalDelta(parts, ordinaryText.String())
			finished = true
		case "FAILED", "SKIPPED":
			return contract.Fail(502, "FAILED_PRECONDITION", "Business did not complete the answer")
		case "", "IN_PROGRESS":
		default:
			return malformedStream()
		}
		if len(parts) > 0 {
			for _, part := range parts {
				outputBytes += len(part.Text) + len(part.ThoughtSignature)
				if part.InlineData != nil {
					outputBytes += len(part.InlineData.Data) + base64.StdEncoding.EncodedLen(len(part.InlineData.Data))
				}
			}
			if outputBytes > 4<<20 {
				return contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4 MiB")
			}
			if err := emit(contract.Event{Parts: parts}); err != nil {
				return err
			}
			for _, part := range parts {
				if !part.Thought && part.ThoughtSignature == "" && part.InlineData == nil {
					ordinaryText.WriteString(part.Text)
				}
			}
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim(']') || !finished {
		return malformedStream()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return malformedStream()
	}
	return emit(contract.Event{Finished: true})
}

// A terminal answer can repeat the full ordinary-text answer after incremental
// deltas. Only strip an exact, already-emitted ordinary-text prefix from an
// unsigned terminal frame. Inline data remains in place; thought/signature
// frames keep all their boundaries without text merging.
func terminalDelta(parts []contract.Part, previous string) []contract.Part {
	if previous == "" || len(parts) == 0 {
		return parts
	}
	var combined strings.Builder
	for _, part := range parts {
		if part.Thought || part.ThoughtSignature != "" {
			return parts
		}
		if part.InlineData != nil {
			continue
		}
		combined.WriteString(part.Text)
	}
	if !strings.HasPrefix(combined.String(), previous) {
		return parts
	}
	remaining := len(previous)
	result := make([]contract.Part, 0, len(parts))
	for _, part := range parts {
		if part.InlineData != nil {
			result = append(result, part)
			continue
		}
		if remaining >= len(part.Text) {
			remaining -= len(part.Text)
			continue
		}
		part.Text = part.Text[remaining:]
		remaining = 0
		result = append(result, part)
	}
	return result
}

func decodeSuggestions(raw json.RawMessage) (*contract.InlineData, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, malformedStream()
	}
	fields := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || (name != "mimeType" && name != "data") {
			return nil, malformedStream()
		}
		if _, exists := fields[name]; exists {
			return nil, malformedStream()
		}
		var value string
		if decoder.Decode(&value) != nil || value == "" {
			return nil, malformedStream()
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF || len(fields) != 2 {
		return nil, malformedStream()
	}
	if fields["mimeType"] != contract.SuggestionsMIMEType {
		return nil, contract.Fail(502, "UNIMPLEMENTED", "Business returned an unsupported inline content type")
	}
	encoded := fields["data"]
	if len(encoded) > 4<<20 {
		return nil, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4 MiB")
	}
	if strings.ContainsAny(encoded, " \r\n\t") {
		return nil, malformedStream()
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || !utf8.Valid(data) || !json.Valid(data) {
		return nil, malformedStream()
	}
	if len(data)+len(encoded) > 4<<20 {
		return nil, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4 MiB")
	}
	return &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: data}, nil
}

func malformedStream() error {
	return contract.Fail(502, "DATA_LOSS", "Business response is malformed or incomplete")
}
