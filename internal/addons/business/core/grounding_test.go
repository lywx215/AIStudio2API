package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func groundingFixture(t *testing.T, references []any, segments []any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"references": references, "segments": segments})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func groundingReference(uri, title string) any {
	return map[string]any{"documentMetadata": map[string]any{"uri": uri, "title": title}}
}

func groundingSegment(start, end any, text string, indices ...int) any {
	return map[string]any{"startIndex": start, "endIndex": end, "text": text, "referenceIndices": indices}
}

func TestGroundingUTF8PartsAndStableSourceIndices(t *testing.T) {
	var collector groundingCollector
	first := groundingFixture(t, []any{groundingReference("https://example.test/a", "A")}, []any{groundingSegment("3", "7", "🙂", 0)})
	if err := collector.add(first, "中🙂A", 2); err != nil {
		t.Fatal(err)
	}
	second := groundingFixture(t, []any{groundingReference("https://example.test/b", "B"), groundingReference("https://example.test/a", "changed")}, []any{groundingSegment(0, 3, "é", 1, 0, 1)})
	if err := collector.add(second, "é", 4); err != nil {
		t.Fatal(err)
	}
	// Identical metadata does not append sources or supports twice.
	if err := collector.add(second, "é", 4); err != nil {
		t.Fatal(err)
	}
	got, err := collector.result()
	if err != nil || got == nil || len(got.GroundingChunks) != 2 || len(got.GroundingSupports) != 2 {
		t.Fatalf("unexpected grounding: %#v %v", got, err)
	}
	if got.GroundingChunks[0].RetrievedContext.Title != "A" || got.GroundingChunks[1].RetrievedContext.URI != "https://example.test/b" {
		t.Fatal("source identity/order changed")
	}
	if got.GroundingSupports[0].Segment != (contract.GroundingSegment{PartIndex: 2, StartIndex: 3, EndIndex: 7, Text: "🙂"}) {
		t.Fatal("UTF-8 bytes or Part identity lost")
	}
	if got.GroundingSupports[1].Segment.PartIndex != 4 || !reflect.DeepEqual(got.GroundingSupports[1].GroundingChunkIndices, []int{0, 1}) {
		t.Fatal("global source remapping failed")
	}
	encoded, _ := json.Marshal(got)
	for _, field := range []string{"confidenceScores", "searchEntryPoint", "webSearchQueries", `"web":`, "fileSearchStore", "documentName"} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("invented %s", field)
		}
	}
	if !strings.Contains(string(encoded), `"retrievedContext":`) {
		t.Fatal("document source was not projected as retrieved context")
	}
}

func TestGroundingUnanchoredMetadataRetainsSourcesOnly(t *testing.T) {
	for _, raw := range []string{
		`{"references":[{"documentMetadata":{"uri":"https://example.test/a"}}],"segments":[{"startIndex":"900","endIndex":"999","text":"not the latest reply","referenceIndices":[0]}]}`,
		`{"references":[{"documentMetadata":{"uri":"https://example.test/a"}}],"segments":{"uninterpreted":true}}`,
	} {
		var collector groundingCollector
		if err := collector.add(json.RawMessage(raw), "a different last text", -1); err != nil {
			t.Fatal(err)
		}
		got, err := collector.result()
		if err != nil || got == nil || len(got.GroundingChunks) != 1 || len(got.GroundingSupports) != 0 {
			t.Fatal("unanchored metadata acquired a guessed support", got, err)
		}
	}
}

func TestGroundingOnlyWebReferencesAndZeroDefault(t *testing.T) {
	var collector groundingCollector
	refs := []any{
		groundingReference("gs://private-bucket/object", "storage"),
		groundingReference("https://user:secret@example.test/private", "credential"),
		groundingReference("javascript:alert(1)", "script"),
		groundingReference("/relative", "relative"),
		groundingReference("https:///missing-host", "missing"),
		map[string]any{"content": "document without URI"},
		groundingReference("https://example.test/a#fragment", "valid"),
		groundingReference("http://example.test/b", "valid http"),
	}
	segment := map[string]any{"endIndex": "1", "text": "x", "referenceIndices": []int{0, 1, 2, 3, 4, 5, 6, 7}}
	if err := collector.add(groundingFixture(t, refs, []any{segment}), "x", 0); err != nil {
		t.Fatal(err)
	}
	got, err := collector.result()
	if err != nil || len(got.GroundingChunks) != 2 || len(got.GroundingSupports) != 1 {
		t.Fatal("unsupported references became web or lost valid references", got, err)
	}
	if !reflect.DeepEqual(got.GroundingSupports[0].GroundingChunkIndices, []int{0, 1}) {
		t.Fatal("filtered reference indices were not remapped")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private-bucket") {
		t.Fatal("unmapped private URI leaked")
	}
}

func TestGroundingEmptyAndUnmappedMetadata(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`), json.RawMessage(`{"references":[],"segments":[]}`), json.RawMessage(`{"references":[{"documentMetadata":{"uri":"gs://bucket/key"}}]}`)} {
		var collector groundingCollector
		if err := collector.add(raw, "", -1); err != nil {
			t.Fatal(err)
		}
		if got, err := collector.result(); err != nil || got != nil {
			t.Fatal("empty grounding invented metadata", got, err)
		}
	}
}

func TestGroundingWebSharedFieldBoundaries(t *testing.T) {
	prefix := "HTTPS://example.test/"
	maxURI := prefix + strings.Repeat("x", contract.MaxGroundingURIBytes-len(prefix))
	maxTitle := strings.Repeat("界", 1365) + "x" // 4096 UTF-8 bytes.
	web := &contract.GroundingWeb{URI: maxURI, Title: maxTitle}
	if !contract.ValidGroundingWebURI(maxURI) || !contract.ValidGroundingWeb(web) {
		t.Fatal("exact limits or case-insensitive scheme were rejected")
	}
	if !contract.ValidGroundingRetrievedContext(&contract.GroundingRetrievedContext{URI: maxURI, Title: maxTitle}) {
		t.Fatal("retrieved context field rules differ from existing URI limits")
	}
	var collector groundingCollector
	if err := collector.add(groundingFixture(t, []any{groundingReference(maxURI, maxTitle)}, nil), "", -1); err != nil {
		t.Fatal(err)
	}
	got, err := collector.result()
	if err != nil || got.GroundingChunks[0].RetrievedContext.URI != maxURI || got.GroundingChunks[0].RetrievedContext.Title != maxTitle {
		t.Fatal("accepted source was rewritten", err)
	}
	for _, over := range []*contract.GroundingWeb{
		{URI: maxURI + "x", Title: "title"},
		{URI: "https://example.test", Title: maxTitle + "x"},
	} {
		if contract.ValidGroundingWeb(over) {
			t.Fatal("shared validator allowed oversized source")
		}
		if contract.ValidGroundingRetrievedContext(&contract.GroundingRetrievedContext{URI: over.URI, Title: over.Title}) {
			t.Fatal("retrieved context allowed oversized source")
		}
		var rejected groundingCollector
		err := rejected.add(groundingFixture(t, []any{groundingReference(over.URI, over.Title)}, nil), "", -1)
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Status != "DATA_LOSS" {
			t.Fatal("oversized web source was not rejected at collection", err)
		}
	}
	for _, uri := range []string{"http://example.test/a", "HtTpS://example.test/a"} {
		if !contract.ValidGroundingWeb(&contract.GroundingWeb{URI: uri}) {
			t.Fatal("valid web source rejected", uri)
		}
	}
	for _, uri := range []string{" https://example.test", "https://example.test ", "https://example.test/\r\n", "https://example.test/\t", "https://user:private@example.test", "https:opaque", "gs://bucket/object", "https:///missing", "https://example.test/" + string([]byte{0xff})} {
		if contract.ValidGroundingWebURI(uri) || contract.ValidGroundingWeb(&contract.GroundingWeb{URI: uri}) {
			t.Fatal("invalid or non-web URI accepted")
		}
	}
	if contract.ValidGroundingWeb(nil) || contract.ValidGroundingWeb(&contract.GroundingWeb{URI: "https://example.test", Title: string([]byte{0xff})}) {
		t.Fatal("nil source or invalid title UTF-8 accepted")
	}
}

func TestGroundingProjectedSpanSplittingPreservesSourceBytes(t *testing.T) {
	var collector groundingCollector
	text := "A中🙂Z"
	spans := []streamTextSpan{
		{SourceStart: 0, SourceEnd: 4, PartIndex: 1, PartStart: 2},
		{SourceStart: 4, SourceEnd: 8, PartIndex: 3, PartStart: 0},
		{SourceStart: 8, SourceEnd: 9, PartIndex: 4, PartStart: 5},
	}
	raw := groundingFixture(t, []any{groundingReference("https://example.test/document", "Doc")}, []any{groundingSegment("1", "9", "中🙂Z", 0)})
	for i := 0; i < 2; i++ {
		if err := collector.addProjected(raw, text, spans); err != nil {
			t.Fatal(err)
		}
	}
	got, err := collector.result()
	if err != nil || len(got.GroundingSupports) != 3 || len(got.GroundingChunks) != 1 || got.GroundingChunks[0].Web != nil || got.GroundingChunks[0].RetrievedContext == nil {
		t.Fatal("split grounding lost source type, identity, or deduplication", got, err)
	}
	want := []contract.GroundingSegment{
		{PartIndex: 1, StartIndex: 3, EndIndex: 6, Text: "中"},
		{PartIndex: 3, StartIndex: 0, EndIndex: 4, Text: "🙂"},
		{PartIndex: 4, StartIndex: 5, EndIndex: 6, Text: "Z"},
	}
	for i, support := range got.GroundingSupports {
		if support.Segment != want[i] || !reflect.DeepEqual(support.GroundingChunkIndices, []int{0}) {
			t.Fatalf("wrong projected support %d: %#v", i, support)
		}
	}
}

func TestGroundingProjectedRejectsUnprovenCoverage(t *testing.T) {
	raw := groundingFixture(t, []any{groundingReference("https://example.test/document", "")}, []any{groundingSegment(0, 3, "abc", 0)})
	for name, spans := range map[string][]streamTextSpan{
		"gap":             {{SourceStart: 0, SourceEnd: 1, PartIndex: 0}, {SourceStart: 2, SourceEnd: 3, PartIndex: 1}},
		"missing_prefix":  {{SourceStart: 1, SourceEnd: 3, PartIndex: 0}},
		"missing_suffix":  {{SourceStart: 0, SourceEnd: 2, PartIndex: 0}},
		"overlap":         {{SourceStart: 0, SourceEnd: 2, PartIndex: 0}, {SourceStart: 1, SourceEnd: 3, PartIndex: 1}},
		"source_order":    {{SourceStart: 1, SourceEnd: 3, PartIndex: 0}, {SourceStart: 0, SourceEnd: 1, PartIndex: 1}},
		"target_order":    {{SourceStart: 0, SourceEnd: 1, PartIndex: 1}, {SourceStart: 1, SourceEnd: 3, PartIndex: 0}},
		"target_overlap":  {{SourceStart: 0, SourceEnd: 1, PartIndex: 0}, {SourceStart: 1, SourceEnd: 3, PartIndex: 0}},
		"negative_source": {{SourceStart: -1, SourceEnd: 3, PartIndex: 0}},
		"empty_span":      {{SourceStart: 0, SourceEnd: 0, PartIndex: 0}},
		"source_range":    {{SourceStart: 0, SourceEnd: 4, PartIndex: 0}},
		"negative_target": {{SourceStart: 0, SourceEnd: 3, PartIndex: -1}},
		"part_range":      {{SourceStart: 0, SourceEnd: 3, PartIndex: maxStreamParts}},
		"negative_offset": {{SourceStart: 0, SourceEnd: 3, PartIndex: 0, PartStart: -1}},
		"offset_overflow": {{SourceStart: 0, SourceEnd: 3, PartIndex: 0, PartStart: int(^uint(0) >> 1)}},
	} {
		t.Run(name, func(t *testing.T) {
			var collector groundingCollector
			err := collector.addProjected(raw, "abc", spans)
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Status != "DATA_LOSS" || failure.Message != "Business grounding metadata is malformed" {
				t.Fatal("unproven mapping did not fail safely", err)
			}
			if got, resultErr := collector.result(); got != nil || resultErr != err {
				t.Fatal("partial mapping escaped after failure")
			}
		})
	}
	unicodeRaw := groundingFixture(t, []any{groundingReference("https://example.test/document", "")}, []any{groundingSegment(0, 3, "中", 0)})
	var splitRune groundingCollector
	if err := splitRune.addProjected(unicodeRaw, "中", []streamTextSpan{{SourceEnd: 1}, {SourceStart: 1, SourceEnd: 3, PartIndex: 1}}); err == nil {
		t.Fatal("span split inside a UTF-8 character was accepted")
	}
	var unanchored groundingCollector
	if err := unanchored.addProjected(raw, "unrelated text", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := unanchored.result(); err != nil || len(got.GroundingChunks) != 1 || len(got.GroundingSupports) != 0 {
		t.Fatal("nil mapping guessed support positions", got, err)
	}
}

func TestGroundingProjectedSplitLimitsAndDuplicateBudget(t *testing.T) {
	for _, size := range []int{contract.MaxGroundingSupports, contract.MaxGroundingSupports + 1} {
		var collector groundingCollector
		text := strings.Repeat("x", size)
		spans := make([]streamTextSpan, size)
		for i := range spans {
			spans[i] = streamTextSpan{SourceStart: i, SourceEnd: i + 1, PartIndex: i}
		}
		raw := groundingFixture(t, []any{groundingReference("https://example.test/document", "")}, []any{groundingSegment(0, size, text, 0)})
		err := collector.addProjected(raw, text, spans)
		if size > contract.MaxGroundingSupports {
			assertGroundingLimit(t, err)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := collector.addProjected(raw, text, spans); err != nil {
			t.Fatal("duplicate split supports consumed quota", err)
		}
		if got, err := collector.result(); err != nil || len(got.GroundingSupports) != size {
			t.Fatal("exact support limit was not retained", err)
		}
	}
	var bytesCollector groundingCollector
	text := strings.Repeat("<", 4096)
	raw := groundingFixture(t, []any{groundingReference("https://example.test/document", "")}, []any{groundingSegment(0, len(text), text, 0)})
	for i := 0; i <= 21; i++ {
		err := bytesCollector.addProjected(raw, text, []streamTextSpan{{SourceEnd: len(text), PartIndex: i}})
		if i < 21 && err != nil {
			t.Fatal(err)
		}
		if i == 21 {
			assertGroundingLimit(t, err)
		}
	}
}

func TestGroundingRejectsInvalidAnchorsAndStructures(t *testing.T) {
	const reference = `"references":[{"documentMetadata":{"uri":"https://example.test/private-value"}}]`
	for name, body := range map[string]string{
		"negative":       `{"segments":[{"startIndex":"-1","endIndex":"3","referenceIndices":[0]}],` + reference + `}`,
		"overflow":       `{"segments":[{"endIndex":"9223372036854775808","referenceIndices":[0]}],` + reference + `}`,
		"fraction":       `{"segments":[{"endIndex":1.5,"referenceIndices":[0]}],` + reference + `}`,
		"float_form":     `{"segments":[{"endIndex":3e0,"referenceIndices":[0]}],` + reference + `}`,
		"null_offset":    `{"segments":[{"startIndex":null,"endIndex":"3","referenceIndices":[0]}],` + reference + `}`,
		"missing_end":    `{"segments":[{"referenceIndices":[0]}],` + reference + `}`,
		"range":          `{"segments":[{"endIndex":"8","referenceIndices":[0]}],` + reference + `}`,
		"empty_range":    `{"segments":[{"startIndex":"3","endIndex":"3","referenceIndices":[0]}],` + reference + `}`,
		"start_rune":     `{"segments":[{"startIndex":"1","endIndex":"3","referenceIndices":[0]}],` + reference + `}`,
		"end_rune":       `{"segments":[{"endIndex":"2","referenceIndices":[0]}],` + reference + `}`,
		"wrong_text":     `{"segments":[{"endIndex":"3","text":"x","referenceIndices":[0]}],` + reference + `}`,
		"null_text":      `{"segments":[{"endIndex":"3","text":null,"referenceIndices":[0]}],` + reference + `}`,
		"foreign_index":  `{"segments":[{"endIndex":"3","referenceIndices":[1]}],` + reference + `}`,
		"negative_index": `{"segments":[{"endIndex":"3","referenceIndices":[-1]}],` + reference + `}`,
		"null_index":     `{"segments":[{"endIndex":"3","referenceIndices":[null]}],` + reference + `}`,
		"string_index":   `{"segments":[{"endIndex":"3","referenceIndices":["0"]}],` + reference + `}`,
		"null_reference": `{"references":[null]}`,
		"bad_document":   `{"references":[{"documentMetadata":[]}]}`,
		"null_uri":       `{"references":[{"documentMetadata":{"uri":null}}]}`,
		"null_title":     `{"references":[{"documentMetadata":{"uri":"https://example.test","title":null}}]}`,
		"array_root":     `[]`,
		"duplicate":      `{"references":[],"references":[]}`,
		"bad_surrogate":  `{"references":[{"documentMetadata":{"uri":"https://example.test/\ud800"}}]}`,
		"bad_utf8":       `{"references":[{"documentMetadata":{"uri":"https://example.test/` + string([]byte{0xff}) + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var collector groundingCollector
			err := collector.add(json.RawMessage(body), "中🙂", 0)
			var failure *contract.Failure
			if !errors.As(err, &failure) || failure.Code != 502 || failure.Status != "DATA_LOSS" || failure.Message != "Business grounding metadata is malformed" {
				t.Fatalf("wanted fixed safe protocol failure, got %v", err)
			}
			if got, later := collector.result(); got != nil || later != err {
				t.Fatal("partially invalid metadata became usable")
			}
		})
	}
}

func TestGroundingLimitsAndDeduplication(t *testing.T) {
	t.Run("chunks", func(t *testing.T) {
		var collector groundingCollector
		for i := 0; i < contract.MaxGroundingChunks; i++ {
			body := groundingFixture(t, []any{groundingReference(fmt.Sprintf("https://example.test/%d", i), "")}, nil)
			if err := collector.add(body, "", -1); err != nil {
				t.Fatal(err)
			}
		}
		duplicate := groundingFixture(t, []any{groundingReference("https://example.test/0", "ignored title change")}, nil)
		if err := collector.add(duplicate, "", -1); err != nil {
			t.Fatal("duplicate consumed chunk quota", err)
		}
		assertGroundingLimit(t, collector.add(groundingFixture(t, []any{groundingReference("https://example.test/overflow", "")}, nil), "", -1))
	})
	t.Run("supports", func(t *testing.T) {
		var collector groundingCollector
		body := groundingFixture(t, []any{groundingReference("https://example.test/a", "")}, []any{groundingSegment(0, 1, "x", 0)})
		for i := 0; i < contract.MaxGroundingSupports; i++ {
			if err := collector.add(body, "x", i); err != nil {
				t.Fatal(err)
			}
		}
		if err := collector.add(body, "x", 0); err != nil {
			t.Fatal("duplicate consumed support quota", err)
		}
		assertGroundingLimit(t, collector.add(body, "x", contract.MaxGroundingSupports))
	})
	t.Run("raw_bytes", func(t *testing.T) {
		var collector groundingCollector
		assertGroundingLimit(t, collector.add(json.RawMessage(strings.Repeat(" ", contract.MaxGroundingBytes+1)), "", -1))
	})
	t.Run("retained_escaped_bytes", func(t *testing.T) {
		var collector groundingCollector
		// JSON escapes each '<', so output bytes must include encoding growth.
		for i := 0; i < 32; i++ {
			raw := json.RawMessage(fmt.Sprintf(`{"references":[{"documentMetadata":{"uri":"https://example.test/%d","title":"%s"}}]}`, i, strings.Repeat("<", contract.MaxGroundingTitleBytes)))
			err := collector.add(raw, "", -1)
			if i < 21 && err != nil {
				t.Fatal(err)
			}
			if i == 21 {
				assertGroundingLimit(t, err)
				return
			}
		}
	})
}

func assertGroundingLimit(t *testing.T, err error) {
	t.Helper()
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 502 || failure.Status != "RESOURCE_EXHAUSTED" || failure.Message != "Business grounding metadata exceeds its limit" {
		t.Fatalf("wanted safe metadata limit failure, got %v", err)
	}
}
