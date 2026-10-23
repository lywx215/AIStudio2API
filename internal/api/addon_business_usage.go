package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// businessUsageLogging attaches only to the Business public handler. Native
// AI Studio requests continue through their original middleware unchanged.
func businessUsageLogging(admin AdminService, next http.Handler) http.Handler {
	return requestLoggingMiddleware(admin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		observer := contract.GenerationObserver{
			Target: func(model, account string) {
				SetAccessLogTarget(ctx, model, account)
				SetAccessLogChannel(ctx, "business")
				MarkAccessLogScheduled(ctx)
			},
			Upstream: func() { MarkAccessLogUpstreamAttempt(ctx) },
			Usage: func(u contract.Usage) {
				SetAccessLogGenerationResult(ctx, &aistudio.Usage{
					InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount,
					ReasoningTokens: u.ThoughtsTokenCount, TotalTokens: u.TotalTokenCount,
					Source: u.CountingSource, Incomplete: u.Incomplete,
				}, 0)
				SetAccessLogFinishReason(ctx, "stop")
			},
			Error: func(err error) { businessUsageError(ctx, err) },
		}
		next.ServeHTTP(w, r.WithContext(contract.WithGenerationObserver(ctx, observer)))
	}))
}

func businessUsageError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	// A streamed Business error can follow HTTP 200. Keep its real safe status
	// in the access record instead of classifying that request as completed.
	if metadata, ok := ctx.Value(accessLogContextKey{}).(*accessLogMetadata); ok {
		var failure *contract.Failure
		code, message := http.StatusBadGateway, "Business stream interrupted"
		if errors.As(err, &failure) {
			code, message = failure.Code, failure.Message
		} else if errors.Is(err, context.Canceled) {
			code, message = 499, "Business request cancelled"
		}
		metadata.mu.Lock()
		metadata.err, metadata.failureStatus = message, code
		metadata.canceled = code == 499
		metadata.finishReason = ""
		metadata.mu.Unlock()
	}
}
