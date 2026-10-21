package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type upstreamLogProbe struct {
	AdminService
	starts []AccessLog
	ends   []AccessLog
}

func (probe *upstreamLogProbe) RecordAccessStart(entry AccessLog) {
	probe.starts = append(probe.starts, entry)
}
func (probe *upstreamLogProbe) RecordAccessLog(entry AccessLog) {
	probe.ends = append(probe.ends, entry)
}

type upstreamBodyProbe struct {
	RequestLedger
	enabled bool
	bodies  []RequestBody
}

func (probe *upstreamBodyProbe) BodyCapture() bool         { return probe.enabled }
func (probe *upstreamBodyProbe) SaveBody(body RequestBody) { probe.bodies = append(probe.bodies, body) }

func TestRequestLogsAndBodiesRequireUpstreamSend(t *testing.T) {
	for _, capture := range []bool{false, true} {
		for _, test := range []struct {
			name string
			sent bool
			err  error
		}{
			{"queue-full", false, &aistudio.RequestQueueFullError{Queued: 4, Limit: 4}},
			{"invalid", false, aistudio.ErrInvalidArgument},
			{"stopped", false, errors.New("not running")},
			{"local-proof-failure", false, errors.New("proof preparation failed")},
			{"queue-cancel", false, context.Canceled},
			{"queue-timeout", false, context.DeadlineExceeded},
			{"success", true, nil},
			{"google-429-zero-tokens", true, &aistudio.RPCError{StatusCode: 429, Message: "quota"}},
			{"google-500", true, &aistudio.RPCError{StatusCode: 500, Message: "upstream failed"}},
			{"connection-failure", true, errors.New("connection reset")},
			{"queue-full-after-upstream", true, &aistudio.RequestQueueFullError{Queued: 4, Limit: 4}},
		} {
			t.Run(test.name+map[bool]string{false: "/no-body", true: "/body"}[capture], func(t *testing.T) {
				admin := &upstreamLogProbe{}
				ledger := &upstreamBodyProbe{enabled: capture}
				handler := requestLoggingMiddleware(admin, requestBodyMiddleware(ledger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.ReadAll(r.Body)
					SetAccessLogTarget(r.Context(), "test-model", "selected-account")
					SetAccessLogChannel(r.Context(), "build")
					StartAccessLog(r.Context())
					if len(admin.starts) != 0 {
						t.Fatal("request was logged before sending")
					}
					if test.sent {
						// Concurrent retries share the same metadata even after background detachment.
						ctx := context.WithoutCancel(r.Context())
						var pending sync.WaitGroup
						for range 8 {
							pending.Add(1)
							go func() { defer pending.Done(); MarkAccessLogUpstreamAttempt(ctx) }()
						}
						pending.Wait()
						SetAccessLogGenerationInput(ctx, aistudio.GenerateRequest{ID: "later-child-generation"})
						AddAccessLogAttempt(ctx, RequestAttempt{Account: "first", Error: "first failure"})
						AddAccessLogAttempt(ctx, RequestAttempt{Account: "second", Error: "second failure"})
					}
					if test.err != nil {
						SetAccessLogError(r.Context(), test.err)
						writeOpenAIRequestError(w, test.err)
					} else {
						_, _ = w.Write([]byte("OK"))
					}
				})))
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("input")))
				if !test.sent {
					if len(admin.starts) != 0 || len(admin.ends) != 0 || len(ledger.bodies) != 0 {
						t.Fatal("local rejection persisted logs or body")
					}
					return
				}
				if len(admin.starts) != 1 || len(admin.ends) != 1 || !admin.ends[0].UpstreamAttempted || len(admin.ends[0].Attempts) != 2 || admin.starts[0].RequestID != admin.ends[0].RequestID {
					t.Fatalf("sent request was lost or duplicated: starts=%d ends=%+v", len(admin.starts), admin.ends)
				}
				if capture {
					if len(ledger.bodies) != 1 || ledger.bodies[0].ID != admin.ends[0].RequestID || ledger.bodies[0].Request != "input" {
						t.Fatal("sent request body was lost or duplicated")
					}
				} else if len(ledger.bodies) != 0 {
					t.Fatal("disabled capture persisted a body")
				}
			})
		}
	}
}

func TestBackgroundCompactionKeepsOneLogWhenMainQueueIsFull(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, rejectAt := range []int{1, 2} {
			t.Run(fmt.Sprintf("stream=%t/reject=%d", stream, rejectAt), func(t *testing.T) {
				admin := &upstreamLogProbe{}
				ledger := &upstreamBodyProbe{enabled: true}
				service := &queueAdmissionService{rejectAt: rejectAt}
				handler := NewHandler(service, Config{Admin: admin, Ledger: ledger})
				body, _ := json.Marshal(map[string]any{
					"model": "test-model", "stream": stream, "background": true,
					"context_management": []any{map[string]any{"type": "compaction", "compact_threshold": 1}},
					"input": []any{
						map[string]any{"role": "user", "content": "old"},
						map[string]any{"role": "assistant", "content": strings.Repeat("x", 85000)},
						map[string]any{"role": "user", "content": "new"},
					},
				})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))))
				if w.Code != 429 || service.calls != rejectAt || service.counts != 1 || w.Flushed {
					t.Fatalf("background response=%d calls=%d counts=%d", w.Code, service.calls, service.counts)
				}
				if len(admin.starts) != 1 || len(admin.ends) != 1 || len(ledger.bodies) != 1 {
					t.Fatalf("starts=%d ends=%d bodies=%d", len(admin.starts), len(admin.ends), len(ledger.bodies))
				}
				if admin.starts[0].RequestID != admin.ends[0].RequestID || ledger.bodies[0].ID != admin.ends[0].RequestID || !admin.ends[0].UpstreamAttempted || admin.ends[0].Status != 429 {
					t.Fatal("compaction and main generation split their request identity")
				}
			})
		}
	}
}
