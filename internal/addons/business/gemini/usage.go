package gemini

import (
	"context"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// countLocalText bounds tokenizer scratch space independently of retained JSON.
// The shared tokenizer is deliberately reused rather than a character ratio.
func countLocalText(ctx context.Context, text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	release, err := resources.Reserve(ctx, 1<<20+int64(len(text))*64)
	if err != nil {
		return 0, err
	}
	defer release()
	count := aistudio.EstimatedTextTokens(text)
	if err := ctx.Err(); err != nil {
		return 0, resources.ContextError(err)
	}
	return count, nil
}

// localInputUsage counts normalized, validated turns. Ignored system/tool
// options and generation parameters are not sent and must not inflate usage.
func (p *historyPlan) localInputUsage(ctx context.Context) (contract.Usage, error) {
	u := contract.Usage{CountingSource: contract.LocalUsageSource, Estimated: true}
	for _, turn := range p.turns {
		u.PromptTokenCount++ // Same per-content overhead as AI Studio's fallback.
		var run strings.Builder
		flush := func() error {
			count, err := countLocalText(ctx, run.String())
			u.PromptTokenCount += count
			run.Reset()
			return err
		}
		for _, part := range turn.parts {
			if part.Text != nil {
				run.WriteString(*part.Text)
				continue
			}
			if err := flush(); err != nil {
				return u, err
			}
			values := []string{}
			switch {
			case part.ExecutableCode != nil:
				values = []string{part.ExecutableCode.Language, part.ExecutableCode.Code}
			case part.CodeExecutionResult != nil:
				values = []string{part.CodeExecutionResult.Outcome, part.CodeExecutionResult.Output}
			default:
				// Like the existing fallback, only reference metadata is countable.
				// Never tokenize base64 bytes or pretend to count a PDF/image.
				values = []string{part.Filename}
				u.Incomplete = true
			}
			for _, value := range values {
				count, err := countLocalText(ctx, value)
				if err != nil {
					return u, err
				}
				u.PromptTokenCount += count
			}
		}
		if err := flush(); err != nil {
			return u, err
		}
	}
	u.TotalTokenCount = u.PromptTokenCount
	return u, nil
}

func localOutputUsage(ctx context.Context, input contract.Usage, parts []contract.Part) (*contract.Usage, error) {
	u := input
	var run strings.Builder
	thought := false
	add := func(value string, reasoning bool) error {
		count, err := countLocalText(ctx, value)
		if reasoning {
			u.ThoughtsTokenCount += count
		} else {
			u.CandidatesTokenCount += count
		}
		return err
	}
	flush := func() error {
		err := add(run.String(), thought)
		run.Reset()
		return err
	}
	for _, part := range parts {
		if part.InlineData == nil && part.ExecutableCode == nil && part.CodeExecutionResult == nil {
			if run.Len() > 0 && thought != part.Thought {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			thought = part.Thought
			run.WriteString(part.Text)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		switch {
		case part.ExecutableCode != nil:
			for _, value := range []string{part.ExecutableCode.Language, part.ExecutableCode.Code} {
				if err := add(value, part.Thought); err != nil {
					return nil, err
				}
			}
		case part.CodeExecutionResult != nil:
			for _, value := range []string{part.CodeExecutionResult.Outcome, part.CodeExecutionResult.Output} {
				if err := add(value, part.Thought); err != nil {
					return nil, err
				}
			}
		default:
			// Suggestions are UI metadata, not a generated model modality.
			if part.InlineData.MIMEType != contract.SuggestionsMIMEType {
				u.Incomplete = true
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	u.TotalTokenCount = u.PromptTokenCount + u.CandidatesTokenCount + u.ThoughtsTokenCount
	return &u, nil
}
