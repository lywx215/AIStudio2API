package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func phase6Citation() *contract.CitationMetadata {
	return &contract.CitationMetadata{CitationSources: []contract.CitationSource{{URI: "https://example.test/license", License: "MIT"}}}
}

func TestPhase6SourceMetadataFinalOnlyAndNoSuccessfulFailure(t *testing.T) {
	for _, action := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
		for _, failAfterFinal := range []bool{false, true} {
			t.Run(action+string(rune('0'+boolIntPhase6(failAfterFinal))), func(t *testing.T) {
				delivered := false
				g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
					contract.OnDelivery(ctx, func(ok bool) { delivered = ok })
					if err := emit(contract.Event{Parts: []contract.Part{{Text: "雨"}, {Text: "A"}}}); err != nil {
						return err
					}
					grounding := &contract.GroundingMetadata{GroundingChunks: []contract.GroundingChunk{{RetrievedContext: &contract.GroundingRetrievedContext{URI: "https://example.test/doc", Title: "Document"}}}, GroundingSupports: []contract.GroundingSupport{{Segment: contract.GroundingSegment{PartIndex: 1, StartIndex: 0, EndIndex: 1, Text: "A"}, GroundingChunkIndices: []int{0}}}}
					if err := emit(contract.Event{Finished: true, GroundingMetadata: grounding, CitationMetadata: phase6Citation()}); err != nil {
						return err
					}
					if failAfterFinal {
						return contract.Fail(502, "DATA_LOSS", "fixture transport failure")
					}
					return nil
				})
				w := httptest.NewRecorder()
				Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+action, strings.NewReader(prompt)))
				body := w.Body.String()
				if failAfterFinal {
					if delivered || strings.Contains(body, `"STOP"`) || strings.Contains(body, "citationMetadata") || strings.Contains(body, "groundingMetadata") {
						t.Fatal("failed generation published final metadata or committed", body)
					}
				} else if !delivered || strings.Count(body, `"citationSources"`) != 1 || strings.Count(body, `"retrievedContext"`) != 1 || strings.Count(body, `"STOP"`) != 1 || strings.Contains(body, `"web"`) {
					t.Fatal("final native metadata missing/repeated/mistyped", body)
				}
			})
		}
	}
}

func boolIntPhase6(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestPhase6RejectsSourceUnionAndCitationBudget(t *testing.T) {
	ctx := context.Background()
	for _, chunk := range []contract.GroundingChunk{
		{},
		{Web: &contract.GroundingWeb{URI: "https://example.test"}, RetrievedContext: &contract.GroundingRetrievedContext{URI: "https://example.test"}},
		{RetrievedContext: &contract.GroundingRetrievedContext{URI: "gs://private/example"}},
		{RetrievedContext: &contract.GroundingRetrievedContext{URI: "https://secret:pass@example.test"}},
	} {
		if _, err := validateOutputGrounding(ctx, &contract.GroundingMetadata{GroundingChunks: []contract.GroundingChunk{chunk}}, nil); err == nil {
			t.Fatal("invalid source union or target accepted")
		}
	}
	for _, metadata := range []*contract.CitationMetadata{
		{CitationSources: make([]contract.CitationSource, contract.MaxCitationSources+1)},
		{CitationSources: []contract.CitationSource{{URI: "javascript:alert(1)"}}},
		{CitationSources: []contract.CitationSource{{URI: "https://example.test", License: string([]byte{0xff})}}},
	} {
		if _, err := validateOutputCitation(ctx, metadata); err == nil {
			t.Fatal("invalid citation accepted")
		}
	}
	large := &contract.CitationMetadata{}
	for i := 0; i < 6; i++ {
		large.CitationSources = append(large.CitationSources, contract.CitationSource{URI: "https://example.test", License: strings.Repeat("\x00", contract.MaxCitationLicenseBytes)})
	}
	if _, err := validateOutputCitation(ctx, large); err == nil {
		t.Fatal("escaped citation bytes exceeded bound")
	}
	budget := resources.New(2*contract.MaxCitationBytes - 1)
	limited, lease := budget.Begin(ctx)
	defer lease.Close()
	if _, err := validateOutputCitation(limited, phase6Citation()); !errors.Is(err, resources.ErrExhausted) || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("citation encoding ignored/released budget", err)
	}
	budget.SetLimit(2 * contract.MaxCitationBytes)
	if _, err := validateOutputCitation(limited, phase6Citation()); err != nil || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("citation temporary reservation was not released", err)
	}
}

func TestPhase6CitationOnlyAtCompletionAndCombinedOutputLimit(t *testing.T) {
	for _, early := range []bool{true, false} {
		g := generatorFunc(func(_ context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
			if early {
				return emit(contract.Event{Parts: []contract.Part{{Text: "x"}}, CitationMetadata: phase6Citation()})
			}
			encoded, _ := json.Marshal(phase6Citation())
			return emit(contract.Event{Parts: []contract.Part{{Text: strings.Repeat("x", (4<<20)-len(encoded)+1)}}, CitationMetadata: phase6Citation(), Finished: true})
		})
		w := httptest.NewRecorder()
		Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(prompt)))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"STOP"`) {
			t.Fatal("early or over-budget metadata produced success", w.Code)
		}
	}
}
