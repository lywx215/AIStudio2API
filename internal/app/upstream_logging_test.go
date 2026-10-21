package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/requestdb"
)

type ledgerUpstreamService struct {
	aistudio.Service
	progress func(context.Context, string)
}

func (service ledgerUpstreamService) Generate(ctx context.Context, request aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	api.SetAccessLogTarget(ctx, request.Model, "synthetic-account")
	api.SetAccessLogChannel(ctx, "build")
	api.MarkAccessLogUpstreamAttempt(ctx)
	api.MarkAccessLogUpstreamAttempt(ctx)
	if service.progress != nil {
		service.progress(ctx, "child-generation-id")
	}
	switch request.Model {
	case "google-429":
		return nil, &aistudio.RPCError{StatusCode: 429, Message: "synthetic upstream quota"}
	case "queue-after-google":
		api.AddAccessLogAttempt(ctx, api.RequestAttempt{Account: "synthetic-account", Error: "prior upstream attempt"})
		return nil, &aistudio.RequestQueueFullError{Queued: 4, Limit: 4}
	}
	events := make(chan aistudio.Event, 2)
	events <- aistudio.Event{Kind: aistudio.EventText, Text: "OK"}
	events <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "stop"}
	close(events)
	return events, nil
}

func TestUpstreamOnlyLoggingAndLedger(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run(fmt.Sprint(capture), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requests.db")
			store, err := requestdb.Open(path, func(level, message string) { t.Errorf("%s %s", level, message) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			store.SetBodyCapture(capture)
			service, upstream := newDispatchFixture(t, 2, 2, "busy")
			registry := service.requests
			admin := &runtimeAdmin{requests: registry}
			manager := &runtimeManager{current: &runtimeGeneration{admin: admin}, ledger: store, requests: registry}
			handler := api.NewHandler(service, api.Config{Admin: manager, Ledger: store})
			request := func(handler http.Handler, model string) {
				t.Helper()
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
				if w.Code != 429 && !(model == "google-success" && w.Code == 200) {
					t.Fatalf("response=%d %s", w.Code, w.Body)
				}
			}
			for range 100 {
				request(handler, "queue-model")
			}
			// The runtime entry points also reject local logs when called directly.
			local := api.AccessLog{RequestID: "local", Status: 429, Method: http.MethodPost, Authorized: true}
			manager.RecordAccessStart(local)
			manager.RecordAccessLog(local)
			registry.logRequestProgress(t.Context(), "local", "request", "WARN", "local preparation")
			if len(registry.logs) != 0 || registry.count() != 0 || upstream.calls.Load() != 0 {
				t.Fatal("local queue overflow generated request logs or left active work")
			}
			handler = api.NewHandler(ledgerUpstreamService{progress: func(ctx context.Context, id string) {
				registry.logRequestProgress(ctx, id, "request", "INFO", "upstream sent")
			}}, api.Config{Admin: manager, Ledger: store})
			for _, model := range []string{"google-success", "google-429", "queue-after-google"} {
				request(handler, model)
			}
			if len(registry.logs) != 9 {
				t.Fatalf("expected start/progress/finish per sent request, got %d", len(registry.logs))
			}
			for i := 0; i < len(registry.logs); i += 3 {
				if registry.logs[i].Request.ID != registry.logs[i+1].Request.ID || registry.logs[i].Request.ID != registry.logs[i+2].Request.ID {
					t.Fatal("request progress did not use the shared log identity")
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := requestdb.Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			now := time.Now().UTC()
			query := api.UsageQuery{From: now.Add(-time.Hour), To: now.Add(time.Second), Bucket: time.Minute, Location: time.UTC}
			report, err := reopened.Usage(t.Context(), query)
			if err != nil || report.Totals.Requests != 3 || report.Totals.Failed != 2 || report.Totals.RateLimited != 2 {
				t.Fatalf("upstream usage=%+v err=%v", report.Totals, err)
			}
			page, err := reopened.Records(t.Context(), api.UsageRecordQuery{From: query.From, To: query.To, Limit: 100})
			if err != nil || len(page.Items) != 3 {
				t.Fatalf("records=%+v err=%v", page, err)
			}
			for _, item := range page.Items {
				if item.HasBody != capture || item.Model == "queue-model" {
					t.Fatalf("incorrect persisted record: %+v", item)
				}
				_, err := reopened.RequestBody(t.Context(), item.ID)
				if capture && err != nil || !capture && !errors.Is(err, api.ErrRequestBodyNotFound) {
					t.Fatalf("body capture=%t err=%v", capture, err)
				}
			}
		})
	}
}
