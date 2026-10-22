package gemini

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

type candidate struct {
	Content      content `json:"content"`
	Index        int     `json:"index"`
	FinishReason string  `json:"finishReason,omitempty"`
}
type content struct {
	Role  string          `json:"role"`
	Parts []contract.Part `json:"parts"`
}
type response struct {
	Candidates   []candidate `json:"candidates"`
	ModelVersion string      `json:"modelVersion,omitempty"`
}

func responseFor(parts []contract.Part, finished bool) response {
	if parts == nil {
		parts = []contract.Part{}
	}
	c := candidate{Content: content{Role: "model", Parts: parts}, Index: 0}
	if finished {
		c.FinishReason = "STOP"
	}
	return response{Candidates: []candidate{c}}
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
				v, err := strconv.Atoi(raw)
				if err != nil || v < 1 || v > 1000 {
					Error(w, invalid("pageSize must be between 1 and 1000"))
					return
				}
				limit = v
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
			Error(w, contract.Fail(501, "UNIMPLEMENTED", "This Business model method is not available"))
			return
		}
		_ = control.SetReadDeadline(time.Now().Add(15 * time.Second))
		input, err := ParseWithFiles(r.Context(), r.Body, *binding, resolver)
		_ = control.SetReadDeadline(time.Time{})
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
		started := false
		finished := false
		first := true
		total := 0
		parts := []contract.Part{}
		var terminal []contract.Part
		var emitFailure error
		selection := contract.Selection{ModelID: binding.UpstreamModelID, Auto: binding.SelectorMode == "auto", AccountIDs: binding.AccountIDs}
		writeEvent := func(eventParts []contract.Part, done bool) error {
			data, marshalErr := json.Marshal(responseFor(eventParts, done))
			if marshalErr != nil {
				return contract.Fail(502, "DATA_LOSS", "Business output cannot be encoded")
			}
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
			if alt == "sse" {
				_, err = fmt.Fprintf(w, "data: %s\n\n", data)
			} else {
				if !first {
					_, err = w.Write([]byte(","))
				}
				if err == nil {
					_, err = w.Write(data)
				}
			}
			first = false
			if err != nil {
				return err
			}
			if err = control.Flush(); err != nil {
				return err
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
			for _, p := range event.Parts {
				total += len(p.Text) + len(p.ThoughtSignature)
				if p.InlineData != nil {
					if p.Text != "" || p.Thought || p.ThoughtSignature != "" || p.InlineData.MIMEType != contract.SuggestionsMIMEType || !utf8.Valid(p.InlineData.Data) || !json.Valid(p.InlineData.Data) {
						return contract.Fail(502, "DATA_LOSS", "Business returned an invalid inline content part")
					}
					total += len(p.InlineData.Data) + base64.StdEncoding.EncodedLen(len(p.InlineData.Data))
				}
			}
			if total > 4<<20 {
				return contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4 MiB")
			}
			if !stream {
				parts = append(parts, event.Parts...)
				return nil
			}
			if event.Finished {
				// A terminal frame is released only after Generate itself succeeds.
				// A later transport/parser error must never carry a STOP reason.
				terminal = append([]contract.Part(nil), event.Parts...)
				return nil
			}
			return writeEvent(event.Parts, false)
		}
		err = generator.Generate(r.Context(), selection, input, emit)
		if err == nil {
			err = emitFailure
		}
		if err == nil && !finished {
			err = contract.Fail(502, "DATA_LOSS", "Business stream ended without completion")
		}
		if err == nil && stream {
			err = writeEvent(terminal, true)
		}
		if err != nil {
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
			_ = control.SetWriteDeadline(time.Now().Add(15 * time.Second))
			write(w, responseFor(parts, true))
		} else if alt != "sse" {
			_, _ = w.Write([]byte("]"))
		}
	})
}
func model(b config.Binding) any {
	return map[string]any{"name": "models/" + b.PublicID, "displayName": b.DisplayName, "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}}
}
func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
