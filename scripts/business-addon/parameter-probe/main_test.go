package main

import (
	"encoding/json"
	"testing"
)

func TestUnknownFieldsStreamingErrorEnvelope(t *testing.T) {
	var payload any
	if err := json.Unmarshal([]byte(`[{"error":{"message":"Unknown name \"temperature\" at 'stream_assist_request.assist_generation_config'","details":[{"description":"Unknown name \"topP\"; synthetic private text must not escape"}]}}]`), &payload); err != nil {
		t.Fatal(err)
	}
	fields := map[string]json.RawMessage{"temperature": json.RawMessage(`0`), "topP": json.RawMessage(`0.5`)}
	found := map[string]bool{}
	collectUnknownFields(payload, fields, found)
	if len(found) != 2 || !found["temperature"] || !found["topP"] {
		t.Fatal(found)
	}
}
