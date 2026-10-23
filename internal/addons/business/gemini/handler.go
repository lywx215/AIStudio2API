package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

type candidate struct {
	Content           content                     `json:"content"`
	Index             int                         `json:"index"`
	FinishReason      string                      `json:"finishReason,omitempty"`
	GroundingMetadata *contract.GroundingMetadata `json:"groundingMetadata,omitempty"`
	CitationMetadata  *contract.CitationMetadata  `json:"citationMetadata,omitempty"`
}
type content struct {
	Role  string          `json:"role"`
	Parts []contract.Part `json:"parts"`
}
type response struct {
	Candidates    []candidate     `json:"candidates"`
	ModelVersion  string          `json:"modelVersion,omitempty"`
	UsageMetadata *contract.Usage `json:"usageMetadata,omitempty"`
}

type responseMetadata struct {
	grounding *contract.GroundingMetadata
	citation  *contract.CitationMetadata
	usage     *contract.Usage
}

func responseFor(parts []contract.Part, finished bool, metadata ...responseMetadata) response {
	if parts == nil {
		parts = []contract.Part{}
	}
	c := candidate{Content: content{Role: "model", Parts: parts}, Index: 0}
	if finished {
		c.FinishReason = "STOP"
		if len(metadata) > 0 {
			c.GroundingMetadata = metadata[0].grounding
			c.CitationMetadata = metadata[0].citation
		}
	}
	result := response{Candidates: []candidate{c}}
	if finished && len(metadata) > 0 {
		result.UsageMetadata = metadata[0].usage
	}
	return result
}
func Error(w http.ResponseWriter, err error) {
	var f *contract.Failure
	if errors.As(err, &f) {
		contract.Error(w, f.Code, f.Status, f.Message)
	} else {
		contract.Error(w, 500, "INTERNAL", "Business request failed")
	}
}

func Handler(bindings []config.Binding, generator contract.Generator) http.Handler {
	return HandlerWithFiles(bindings, generator, nil)
}

// HandlerWithFiles resolves only this Business runtime's file references. The
// compatibility Handler intentionally has no access to a file store.
func HandlerWithFiles(bindings []config.Binding, generator contract.Generator, resolver contract.FileResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		control := http.NewResponseController(w)
		defer control.SetWriteDeadline(time.Time{})
		defer control.SetReadDeadline(time.Time{})
		models := []config.Binding{}
		for _, b := range bindings {
			if b.Enabled {
				models = append(models, b)
			}
		}
		if r.URL.Path == "/v1beta/models" && r.Method == "GET" {
			start := 0
			if token := r.URL.Query().Get("pageToken"); token != "" {
				var err error
				if !strings.HasPrefix(token, "biz-models-") {
					Error(w, invalid("invalid model page token"))
					return
				}
				start, err = strconv.Atoi(strings.TrimPrefix(token, "biz-models-"))
				if err != nil || start < 0 || start > len(models) {
					Error(w, invalid("invalid model page token"))
					return
				}
			}
			limit := 50
			if raw := r.URL.Query().Get("pageSize"); raw != "" {
				v, err := strconv.ParseInt(raw, 10, 32)
				if err != nil || v < 1 {
					Error(w, invalid("pageSize must be a positive 32-bit integer"))
					return
				}
				limit = int(min(v, 1000))
			}
			end := min(start+limit, len(models))
			items := []any{}
			for _, b := range models[start:end] {
				items = append(items, model(b))
			}
			result := map[string]any{"models": items}
			if end < len(models) {
				result["nextPageToken"] = "biz-models-" + strconv.Itoa(end)
			}
			write(w, result)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			Error(w, contract.Fail(501, "UNIMPLEMENTED", "This Business capability is not available"))
			return
		}
		id, action, hasAction := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), ":")
		var binding *config.Binding
		for i := range models {
			if models[i].PublicID == id {
				binding = &models[i]
				break
			}
		}
		if binding == nil {
			Error(w, contract.Fail(404, "NOT_FOUND", "Business model is unknown or disabled"))
			return
		}
		if !hasAction && r.Method == "GET" {
			write(w, model(*binding))
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "GET, POST")
			Error(w, contract.Fail(405, "INVALID_ARGUMENT", "Method not allowed"))
			return
		}
		if action != "generateContent" && action != "streamGenerateContent" {
			if action == "countTokens" {
				Error(w, contract.Fail(501, "UNIMPLEMENTED", "Business has no verified native token-count endpoint; file ingestion counts are not generation usage"))
				return
			}
			Error(w, contract.Fail(501, "UNIMPLEMENTED", "This Business model method is not available"))
			return
		}
		if r.ContentLength > maxRequestBytes {
			Error(w, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business request exceeds 68 MiB"))
			return
		}
		parseCtx, finishRead := generationReadContext(control, r.Context())
		input, err := ParseWithFiles(parseCtx, r.Body, *binding, resolver)
		finishRead()
		if err != nil {
			Error(w, err)
			return
		}
		if generator == nil {
			Error(w, contract.Fail(503, "UNAVAILABLE", "Business account runtime is not ready"))
			return
		}
		stream := action == "streamGenerateContent"
		alt := r.URL.Query().Get("alt")
		if stream && alt != "" && alt != "sse" && alt != "json" {
			Error(w, invalid("stream alt must be sse or json"))
			return
		}
		w.Header().Set("X-AIStudio-Usage-Source", contract.LocalUsageSource)
		w.Header().Set("X-AIStudio-Usage-Estimated", "true")
		var usage *contract.Usage
		started := false
		finished := false
		first := true
		total := 0
		totalParts := 0
		parts := []contract.Part{}
		var terminal []contract.Part
		var pending []contract.Part
		var grounding *contract.GroundingMetadata
		var citation *contract.CitationMetadata
		terminalWriteAttempted := false
		var emitFailure error
		selection := contract.Selection{PublicID: binding.PublicID, ModelID: binding.UpstreamModelID, Auto: binding.SelectorMode == "auto", AccountIDs: binding.AccountIDs}
		deliveryCtx, completeDelivery := contract.WithDelivery(r.Context())
		delivered := false
		defer func() { completeDelivery(delivered) }()
		writeEvent := func(eventParts []contract.Part, done bool) error {
			data, release, marshalErr := marshalResponse(r.Context(), eventParts, done, responseMetadata{grounding: grounding, citation: citation, usage: usage})
			if marshalErr != nil {
				return marshalErr
			}
			defer release()
			control := http.NewResponseController(w)
			_ = control.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if !started {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Accel-Buffering", "no")
				if alt == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				started = true
				if alt != "sse" {
					if _, err := w.Write([]byte("[")); err != nil {
						return err
					}
				}
			}
			var writeErr error
			if alt == "sse" {
				_, writeErr = fmt.Fprintf(w, "data: %s\n\n", data)
			} else {
				if !first {
					_, writeErr = w.Write([]byte(","))
				}
				if writeErr == nil {
					_, writeErr = w.Write(data)
				}
			}
			first = false
			if writeErr != nil {
				return writeErr
			}
			if writeErr = control.Flush(); writeErr != nil {
				return writeErr
			}
			return nil
		}
		emit := func(event contract.Event) (emitErr error) {
			defer func() {
				if emitErr != nil && emitFailure == nil {
					emitFailure = emitErr
				}
			}()
			if emitFailure != nil {
				return emitFailure
			}
			if finished {
				return contract.Fail(502, "DATA_LOSS", "Unexpected event after completion")
			}
			finished = event.Finished
			totalParts += len(event.Parts)
			if totalParts > 4096 {
				return contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4096 parts")
			}
			for _, p := range event.Parts {
				if err := validateOutputPart(p); err != nil {
					return err
				}
				total += p.PayloadBytes()
			}
			if _, err := resources.Reserve(r.Context(), int64(len(event.Parts))*128); err != nil {
				return err
			}
			// Retain bounded references for final grounding offsets, including SSE.
			parts = append(parts, event.Parts...)
			if event.CitationMetadata != nil {
				if !event.Finished {
					return contract.Fail(502, "DATA_LOSS", "Business citations must accompany completion")
				}
				bytes, err := validateOutputCitation(r.Context(), event.CitationMetadata)
				if err != nil {
					return err
				}
				total += bytes
				citation = event.CitationMetadata
			}
			if event.GroundingMetadata != nil {
				if !event.Finished {
					return contract.Fail(502, "DATA_LOSS", "Business grounding must accompany completion")
				}
				bytes, err := validateOutputGrounding(r.Context(), event.GroundingMetadata, parts)
				if err != nil {
					return err
				}
				total += bytes
				grounding = event.GroundingMetadata
			}
			if total > 4<<20 {
				return contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4 MiB")
			}
			if !stream {
				return nil
			}
			available := append(pending, event.Parts...)
			if event.Finished {
				// A terminal frame is released only after Generate itself succeeds.
				// A later transport/parser error must never carry a STOP reason.
				terminal = append([]contract.Part(nil), available...)
				pending = nil
				return nil
			}
			if len(available) == 0 {
				return nil
			}
			// Official Chat SDKs require a real final content part to retain the
			// completed model turn. Hold one original part, never synthesize or
			// repeat text. This costs at most one upstream part of latency.
			pending = append([]contract.Part(nil), available[len(available)-1:]...)
			if len(available) == 1 {
				return nil
			}
			return writeEvent(available[:len(available)-1], false)
		}
		err = generator.Generate(deliveryCtx, selection, input, emit)
		if err == nil {
			err = emitFailure
		}
		if err == nil && !finished {
			err = contract.Fail(502, "DATA_LOSS", "Business stream ended without completion")
		}
		if err == nil {
			usage, err = localOutputUsage(r.Context(), input.InputUsage, parts)
			if err == nil {
				contract.ReportUsage(r.Context(), *usage)
			}
		}
		if err == nil && stream {
			terminalWriteAttempted = true
			err = writeEvent(terminal, true)
		}
		if err != nil {
			if stream && emitFailure == nil && !terminalWriteAttempted {
				unsent := pending
				if finished {
					unsent = terminal
				}
				if len(unsent) > 0 {
					if writeErr := writeEvent(unsent, false); writeErr != nil {
						err = writeErr
					}
				}
			}
			contract.ReportError(r.Context(), err)
			if !started {
				Error(w, err)
				return
			}
			var failure *contract.Failure
			if !errors.As(err, &failure) {
				failure = &contract.Failure{Code: 502, Status: "UNAVAILABLE", Message: "Business stream interrupted"}
			}
			data, _ := json.Marshal(map[string]any{"error": map[string]any{"code": failure.Code, "status": failure.Status, "message": failure.Message}})
			if alt == "sse" {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			} else {
				if !first {
					_, _ = w.Write([]byte(","))
				}
				_, _ = w.Write(data)
				_, _ = w.Write([]byte("]"))
			}
			return
		}
		if !stream {
			data, release, encodeErr := marshalResponse(r.Context(), parts, true, responseMetadata{grounding: grounding, citation: citation, usage: usage})
			if encodeErr != nil {
				contract.ReportError(r.Context(), encodeErr)
				Error(w, encodeErr)
				return
			}
			defer release()
			_ = control.SetWriteDeadline(time.Now().Add(15 * time.Second))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, err = w.Write(data)
		} else if alt != "sse" {
			_, err = w.Write([]byte("]"))
		}
		delivered = err == nil
		contract.ReportError(r.Context(), err)
	})
}

func marshalResponse(ctx context.Context, parts []contract.Part, finished bool, metadata ...responseMetadata) ([]byte, func(), error) {
	// JSON can expand a string sixfold; Marshal also copies its encoder buffer.
	// Charge both encoded buffers before invoking it, including per-part keys.
	bytes := int64(4096 + len(parts)*1024)
	for _, part := range parts {
		bytes += int64(part.PayloadBytes()) * 12
	}
	if finished && len(metadata) > 0 {
		if metadata[0].grounding != nil {
			bytes += 2 * contract.MaxGroundingBytes
		}
		if metadata[0].citation != nil {
			bytes += 2 * contract.MaxCitationBytes
		}
	}
	release, err := resources.Reserve(ctx, bytes)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(responseFor(parts, finished, metadata...))
	if err != nil {
		release()
		return nil, nil, contract.Fail(502, "DATA_LOSS", "Business output cannot be encoded")
	}
	return data, release, nil
}
func model(b config.Binding) any {
	return map[string]any{"name": "models/" + b.PublicID, "displayName": b.DisplayName, "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}}
}

func generationReadContext(control *http.ResponseController, parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	deadline, _ := ctx.Deadline()
	_ = control.SetReadDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = control.SetReadDeadline(time.Now()) })
	return ctx, func() {
		if !stop() {
			<-interrupted
		}
		cancel()
		_ = control.SetReadDeadline(time.Time{})
	}
}
func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
