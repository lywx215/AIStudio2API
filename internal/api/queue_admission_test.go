package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type queueAdmissionService struct {
	aistudio.Service
	rejectAt int
	calls    int
	counts   int
	onStart  func(context.Context)
	events   <-chan aistudio.Event
}

func (service *queueAdmissionService) Generate(ctx context.Context, _ aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	service.calls++
	if service.onStart != nil {
		service.onStart(ctx)
	}
	if service.calls == service.rejectAt {
		return nil, &aistudio.RequestQueueFullError{Queued: 4, Limit: 4}
	}
	if service.events != nil {
		return service.events, nil
	}
	events := make(chan aistudio.Event, 2)
	events <- aistudio.Event{Kind: aistudio.EventText, Text: "summary"}
	events <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "stop"}
	close(events)
	return events, nil
}

func (service *queueAdmissionService) CountTokens(context.Context, aistudio.TokenCountRequest) (aistudio.TokenCount, error) {
	service.counts++
	return aistudio.TokenCount{InputTokens: 100000}, nil
}

type queueAdmissionAdmin struct {
	AdminService
	logs []AccessLog
}

func (*queueAdmissionAdmin) RecordAccessStart(AccessLog) {}
func (admin *queueAdmissionAdmin) RecordAccessLog(entry AccessLog) {
	admin.logs = append(admin.logs, entry)
}

func TestQueueAdmissionHTTP429Protocols(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, protocol := range []struct{ name, path, body, code string }{
			{"chat", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}]`, "rate_limit_exceeded"},
			{"responses", "/v1/responses", `"input":"hello"`, "rate_limit_exceeded"},
			{"messages", "/v1/messages", `"max_tokens":100,"messages":[{"role":"user","content":"hello"}]`, "rate_limit_error"},
			{"interactions", "/v1/interactions", `"input":"hello"`, "RESOURCE_EXHAUSTED"},
			{"gemini", "/v1beta/models/queue-model:generateContent", `"contents":[{"parts":[{"text":"hello"}]}]`, "RESOURCE_EXHAUSTED"},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol.name, stream), func(t *testing.T) {
				path := protocol.path
				if protocol.name == "gemini" && stream {
					path = "/v1beta/models/queue-model:streamGenerateContent?alt=sse"
				}
				service := &queueAdmissionService{rejectAt: 1}
				admin := &queueAdmissionAdmin{}
				handler := NewHandler(service, Config{Admin: admin})
				body := fmt.Sprintf(`{"model":"queue-model","stream":%t,%s}`, stream, protocol.body)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
				if w.Code != 429 || !strings.Contains(w.Body.String(), protocol.code) || strings.Contains(w.Header().Get("Content-Type"), "event-stream") || w.Flushed {
					t.Fatalf("admission response=%d %s", w.Code, w.Body)
				}
				if len(admin.logs) != 1 || admin.logs[0].Status != 429 || !strings.Contains(admin.logs[0].Error, "当前排队 4，上限 4") {
					t.Fatalf("queue diagnostics missing: %+v", admin.logs)
				}
			})
		}
	}
}

func TestBackgroundQueueAdmissionBeforeHeadersAndCompaction(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, stage := range []string{"generate", "compaction", "after-compaction"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, stage), func(t *testing.T) {
				w := httptest.NewRecorder()
				service := &queueAdmissionService{rejectAt: 1, onStart: func(context.Context) {
					if w.Body.Len() != 0 || w.Flushed {
						t.Error("background sent acceptance before admission")
					}
				}}
				request := map[string]any{"model": "queue-model", "input": "hello", "background": true, "stream": stream}
				if stage != "generate" {
					request["context_management"] = []any{map[string]any{"type": "compaction", "compact_threshold": 1}}
					request["input"] = []any{
						map[string]any{"role": "user", "content": "old"},
						map[string]any{"role": "assistant", "content": strings.Repeat("x", 85000)},
						map[string]any{"role": "user", "content": "new"},
					}
				}
				if stage == "after-compaction" {
					service.rejectAt = 2
				}
				body, _ := json.Marshal(request)
				s := &server{service: service, responseStates: newResponseStateStore(), thoughtSignatures: newThoughtSignatureStore()}
				s.handleResponses(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))))
				if w.Code != 429 || !strings.Contains(w.Body.String(), "rate_limit_exceeded") || w.Flushed || strings.Contains(w.Body.String(), "response.queued") {
					t.Fatalf("background rejection=%d %s", w.Code, w.Body)
				}
				if service.calls != service.rejectAt || (stage != "generate" && service.counts != 1) {
					t.Fatalf("wrong rejected stage: generations=%d counts=%d", service.calls, service.counts)
				}
				active := 0
				s.background.Range(func(any, any) bool { active++; return true })
				if active != 0 {
					t.Fatal("rejected background remained active")
				}
			})
		}
	}
}

func TestBackgroundAdmittedResponseStillStoresAndReplays(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			service := &queueAdmissionService{}
			s := &server{service: service, responseStates: newResponseStateStore()}
			request := responsesRequest{Model: "queue-model", Input: json.RawMessage(`"hello"`), Background: true, Stream: stream}
			prepared := responsesPreparation{generate: aistudio.GenerateRequest{ID: "admitted", Model: "queue-model"}}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if stream {
				s.streamBackground(w, r, request, prepared, "admitted")
			} else {
				s.respondBackground(w, r, request, prepared, "admitted")
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), "queued") {
				t.Fatalf("admitted response=%d %s", w.Code, w.Body)
			}
			response, _, exists := s.responseStates.Response("admitted")
			var result map[string]any
			if !exists || json.Unmarshal(response, &result) != nil || result["status"] != "completed" {
				t.Fatalf("background lost result: %s", response)
			}
			if stream {
				r := httptest.NewRequest(http.MethodGet, "/v1/responses/admitted?stream=true", nil)
				r.SetPathValue("response", "admitted")
				replay := httptest.NewRecorder()
				s.handleResponseGet(replay, r)
				if replay.Body.String() != w.Body.String() {
					t.Fatal("background event replay changed")
				}
			}
		})
	}
}

type backgroundAcceptedWriter struct {
	*httptest.ResponseRecorder
	accepted chan struct{}
	once     sync.Once
}

func (writer *backgroundAcceptedWriter) Flush() {
	writer.ResponseRecorder.Flush()
	writer.once.Do(func() { close(writer.accepted) })
}

func TestBackgroundAdmissionDoesNotWaitForGenerationAndPreservesCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, action := range []string{"disconnect", "cancel"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, action), func(t *testing.T) {
				events := make(chan aistudio.Event, 2)
				started := make(chan context.Context, 1)
				service := &queueAdmissionService{events: events, onStart: func(ctx context.Context) { started <- ctx }}
				s := &server{service: service, responseStates: newResponseStateStore()}
				request := responsesRequest{Model: "queue-model", Input: json.RawMessage(`"hello"`), Background: true, Stream: stream}
				prepared := responsesPreparation{generate: aistudio.GenerateRequest{ID: "waiting", Model: "queue-model"}}
				ctx, disconnect := context.WithCancel(t.Context())
				defer disconnect()
				w := &backgroundAcceptedWriter{ResponseRecorder: httptest.NewRecorder(), accepted: make(chan struct{})}
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				done := make(chan struct{})
				go func() {
					defer close(done)
					if stream {
						s.streamBackground(w, r, request, prepared, "waiting")
					} else {
						s.respondBackground(w, r, request, prepared, "waiting")
					}
				}()
				closeEvents := sync.OnceFunc(func() { close(events) })
				defer closeEvents()
				select {
				case <-w.accepted:
				case <-time.After(5 * time.Second):
					t.Fatal("background waited for generation before accepting")
				}
				generationCtx := <-started
				wantStatus := "completed"
				if action == "disconnect" {
					disconnect()
					if generationCtx.Err() != nil {
						t.Fatal("client disconnect canceled admitted generation")
					}
					events <- aistudio.Event{Kind: aistudio.EventText, Text: "done"}
					events <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "stop"}
					closeEvents()
				} else {
					wantStatus = "cancelled"
					r := httptest.NewRequest(http.MethodPost, "/v1/responses/waiting/cancel", nil)
					r.SetPathValue("response", "waiting")
					result := httptest.NewRecorder()
					s.handleResponseCancel(result, r)
					if result.Code != 200 || !strings.Contains(result.Body.String(), `"cancelled"`) {
						t.Fatalf("background cancellation=%d %s", result.Code, result.Body)
					}
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("background did not finish")
				}
				response, _, exists := s.responseStates.Response("waiting")
				var result map[string]any
				if !exists || json.Unmarshal(response, &result) != nil || result["status"] != wantStatus {
					t.Fatalf("background result=%s", response)
				}
			})
		}
	}
}
