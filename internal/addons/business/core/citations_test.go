package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func TestCitationSourcesPreserveAttributionWithoutInventedOffsets(t *testing.T) {
	var collector citationCollector
	raw := json.RawMessage(`{"citations":[{"uri":"https://example.test/a","license":"MIT","startIndex":"4","endIndex":"9","title":"ignored","publicationDate":{}},{"uri":"https://example.test/a","license":"MIT"},{"uri":"https://example.test/a","license":"Apache-2.0"},{"uri":"gs://bucket/a"},{"uri":"https://secret:private@example.test/a"},{"uri":"javascript:alert(1)"},{"license":"no URI"}]}`)
	for i := 0; i < 2; i++ {
		if err := collector.add(raw); err != nil {
			t.Fatal(err)
		}
	}
	result, err := collector.result()
	if err != nil || len(result.CitationSources) != 2 || result.CitationSources[0].License != "MIT" || result.CitationSources[1].License != "Apache-2.0" {
		t.Fatal("attribution order/dedup changed", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "Index") || strings.Contains(string(encoded), "title") || strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), `"citationSources"`) {
		t.Fatal("source-only native wire contract changed", string(encoded), err)
	}
	for _, empty := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`), json.RawMessage(`{"citations":null}`)} {
		var c citationCollector
		if err := c.add(empty); err != nil {
			t.Fatal(err)
		}
		if value, err := c.result(); err != nil || value != nil {
			t.Fatal("empty citation became metadata", value, err)
		}
	}
}

func TestCitationRejectsMalformedWithoutLeakingSource(t *testing.T) {
	for _, raw := range []string{
		`[]`, `{"citations":{}}`, `{"citations":[null]}`, `{"citations":[1]}`,
		`{"citations":[{"uri":null}]}`, `{"citations":[{"uri":1}]}`,
		`{"citations":[{"uri":"https://private.test","license":null}]}`,
		`{"citations":[{"uri":"https://private.test","license":1}]}`,
		`{"citations":[{"uri":"https://private.test","uri":"https://another.test"}]}`,
		`{"citations":[{"uri":"https://private.test","license":"\ud800"}]}`,
		`{"citations":[{"uri":"https://private.test","license":"` + string([]byte{0xff}) + `"}]}`,
	} {
		var collector citationCollector
		if err := collector.add(json.RawMessage(`{"citations":[{"uri":"https://example.test/old"}]}`)); err != nil {
			t.Fatal(err)
		}
		err := collector.add(json.RawMessage(raw))
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Status != "DATA_LOSS" || failure.Message != "Business citation metadata is malformed" {
			t.Fatal("malformed citation classification", err)
		}
		if result, after := collector.result(); result != nil || after == nil {
			t.Fatal("failed collector returned partial metadata")
		}
		if collector.add(json.RawMessage(`{}`)) == nil {
			t.Fatal("later empty data cleared failure")
		}
	}
}

func TestCitationLimitsIncludeEncodedExpansionAndDuplicates(t *testing.T) {
	makeRaw := func(uri, license string) json.RawMessage {
		encoded, _ := json.Marshal(map[string]any{"citations": []any{map[string]string{"uri": uri, "license": license}}})
		return encoded
	}
	var count citationCollector
	for i := 0; i < contract.MaxCitationSources; i++ {
		if err := count.add(makeRaw(fmt.Sprintf("https://example.test/%d", i), "MIT")); err != nil {
			t.Fatal("within source limit", err)
		}
	}
	if err := count.add(makeRaw("https://example.test/0", "MIT")); err != nil {
		t.Fatal("duplicate consumed limit", err)
	}
	var failure *contract.Failure
	if err := count.add(makeRaw("https://example.test/overflow", "MIT")); !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" {
		t.Fatal("source count limit missing", err)
	}
	var encoded citationCollector
	for i := 0; i < 5; i++ {
		if err := encoded.add(makeRaw(fmt.Sprintf("https://example.test/%d", i), strings.Repeat("\x00", contract.MaxCitationLicenseBytes))); err != nil {
			t.Fatal("within encoded budget", err)
		}
	}
	if err := encoded.add(makeRaw("https://example.test/over", strings.Repeat("\x00", contract.MaxCitationLicenseBytes))); !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" {
		t.Fatal("JSON expansion did not consume total budget", err)
	}
	for _, raw := range []json.RawMessage{
		makeRaw("https://example.test/"+strings.Repeat("u", contract.MaxGroundingURIBytes), "MIT"),
		makeRaw("https://example.test/", strings.Repeat("l", contract.MaxCitationLicenseBytes+1)),
	} {
		var c citationCollector
		if err := c.add(raw); !errors.As(err, &failure) || failure.Status != "DATA_LOSS" {
			t.Fatal("field bound missing", err)
		}
	}
	var rawLimit citationCollector
	if err := rawLimit.add(json.RawMessage(strings.Repeat(" ", contract.MaxCitationBytes+1))); !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" {
		t.Fatal("raw citation budget missing", err)
	}
}
