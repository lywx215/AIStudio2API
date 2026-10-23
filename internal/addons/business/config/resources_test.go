package config

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestResourceBudgetBackwardCompatibilityAndValidation(t *testing.T) {
	data, _ := json.Marshal(Default())
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	delete(fields, "max_inflight_bytes")
	old, _ := json.Marshal(fields)
	c, err := Decode(strings.NewReader(string(old)))
	if err != nil || c.MaxInflightBytes != DefaultMaxInflightBytes || c.Validate(nil) != nil {
		t.Fatalf("old schema-1 config lost compatibility: %v", err)
	}
	for _, value := range []string{"null", "0", "-1", "67108863", "67108865", "4294967297", "1.5", `"268435456"`} {
		fields["max_inflight_bytes"] = json.RawMessage(value)
		body, _ := json.Marshal(fields)
		got, err := Decode(strings.NewReader(string(body)))
		if err == nil && got.Validate(nil) == nil {
			t.Fatalf("invalid budget accepted: %s", value)
		}
	}
	for _, value := range []int64{MinInflightBytes, DefaultMaxInflightBytes, MaxInflightBytes} {
		fields["max_inflight_bytes"] = json.RawMessage(strconv.FormatInt(value, 10))
		body, _ := json.Marshal(fields)
		got, err := Decode(strings.NewReader(string(body)))
		if err != nil || got.Validate(nil) != nil || got.MaxInflightBytes != value {
			t.Fatalf("valid budget rejected: %d, %v", value, err)
		}
	}
}

func TestResourceChangeDoesNotChangeAccountLifetime(t *testing.T) {
	c := Default()
	next := c.Clone()
	next.MaxInflightBytes = MinInflightBytes
	change := next.ChangesFrom(c)
	if !change.Resources || change.Activation || change.Routing || change.Scheduling || change.Metadata {
		t.Fatalf("budget change classified incorrectly: %+v", change)
	}
}
