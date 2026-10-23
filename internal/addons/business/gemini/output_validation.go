package gemini

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func badOutput() error {
	return contract.Fail(502, "DATA_LOSS", "Business returned invalid native output")
}
func validateOutputPart(p contract.Part) error {
	if !utf8.ValidString(p.Text) || !utf8.ValidString(p.ThoughtSignature) {
		return badOutput()
	}
	values := 0
	for _, present := range []bool{p.InlineData != nil, p.ExecutableCode != nil, p.CodeExecutionResult != nil} {
		if present {
			values++
		}
	}
	if values > 1 || (values == 1 && p.Text != "") {
		return badOutput()
	}
	if p.InlineData != nil {
		if !contract.ValidOutputInlineData(p.InlineData) ||
			(p.InlineData.MIMEType == contract.SuggestionsMIMEType && (p.Thought || p.ThoughtSignature != "")) {
			return badOutput()
		}
	}
	if p.ExecutableCode != nil {
		if p.ExecutableCode.Language != "PYTHON" || p.ExecutableCode.Code == "" || !utf8.ValidString(p.ExecutableCode.Code) {
			return badOutput()
		}
	}
	if p.CodeExecutionResult != nil {
		if !contract.ValidExecutionOutcome(p.CodeExecutionResult.Outcome) || !utf8.ValidString(p.CodeExecutionResult.Output) {
			return badOutput()
		}
	}
	return nil
}

func validateOutputGrounding(ctx context.Context, g *contract.GroundingMetadata, parts []contract.Part) (int, error) {
	if len(g.GroundingChunks) > contract.MaxGroundingChunks || len(g.GroundingSupports) > contract.MaxGroundingSupports {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business grounding exceeds its limit")
	}
	size := 0
	for _, chunk := range g.GroundingChunks {
		if !contract.ValidGroundingChunk(chunk) {
			return 0, badOutput()
		}
		if chunk.Web != nil {
			size += len(chunk.Web.URI) + len(chunk.Web.Title)
		} else {
			size += len(chunk.RetrievedContext.URI) + len(chunk.RetrievedContext.Title)
		}
	}
	for _, support := range g.GroundingSupports {
		s := support.Segment
		if s.PartIndex < 0 || s.PartIndex >= len(parts) || len(support.GroundingChunkIndices) == 0 || len(support.GroundingChunkIndices) > contract.MaxGroundingChunks {
			return 0, badOutput()
		}
		p := parts[s.PartIndex]
		if p.Thought || p.InlineData != nil || p.ExecutableCode != nil || p.CodeExecutionResult != nil || s.StartIndex < 0 || s.EndIndex <= s.StartIndex || s.EndIndex > len(p.Text) {
			return 0, badOutput()
		}
		segment := p.Text[s.StartIndex:s.EndIndex]
		if !utf8.ValidString(segment) || segment != s.Text {
			return 0, badOutput()
		}
		for _, index := range support.GroundingChunkIndices {
			if index < 0 || index >= len(g.GroundingChunks) {
				return 0, badOutput()
			}
		}
		size += len(s.Text) + len(support.GroundingChunkIndices)*8 + 64
	}
	if size > contract.MaxGroundingBytes {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business grounding exceeds its limit")
	}
	release, err := resources.Reserve(ctx, 2*contract.MaxGroundingBytes)
	if err != nil {
		return 0, err
	}
	defer release()
	encoded, err := json.Marshal(g)
	if err != nil {
		return 0, badOutput()
	}
	if len(encoded) > contract.MaxGroundingBytes {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business grounding exceeds its limit")
	}
	return len(encoded), nil
}

func validateOutputCitation(ctx context.Context, metadata *contract.CitationMetadata) (int, error) {
	if len(metadata.CitationSources) > contract.MaxCitationSources {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business citations exceed their limit")
	}
	size := 0
	for _, source := range metadata.CitationSources {
		if !contract.ValidCitationSource(source) {
			return 0, badOutput()
		}
		size += len(source.URI) + len(source.License)
	}
	if size > contract.MaxCitationBytes {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business citations exceed their limit")
	}
	release, err := resources.Reserve(ctx, 2*contract.MaxCitationBytes)
	if err != nil {
		return 0, err
	}
	defer release()
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return 0, badOutput()
	}
	if len(encoded) > contract.MaxCitationBytes {
		return 0, contract.Fail(502, "RESOURCE_EXHAUSTED", "Business citations exceed their limit")
	}
	return len(encoded), nil
}
