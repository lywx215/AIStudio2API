package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func requireQueueFull(t *testing.T, err error, queued, limit int) {
	t.Helper()
	var full *aistudio.RequestQueueFullError
	if !errors.As(err, &full) || full.Queued != queued || full.Limit != limit || full.HTTPStatus() != 429 || full.ErrorCode() != "rate_limit_exceeded" {
		t.Fatalf("queue rejection: %v; want queued=%d limit=%d", err, queued, limit)
	}
}

func TestDispatchQueueCapacityAndRelease(t *testing.T) {
	for _, test := range []struct{ workers, concurrency, limit int }{
		{10, 2, 4}, {3, 2, 1}, {1, 1, 0}, {1, 2, 0}, {1, 3, 0}, {2, 2, 0},
	} {
		t.Run(fmt.Sprintf("%dx%d", test.workers, test.concurrency), func(t *testing.T) {
			queue := newDispatchQueue()
			queue.setCapacity(test.workers, test.concurrency)
			var waiters []*dispatchWaiter
			for i := range test.limit {
				waiter, err := queue.join(fmt.Sprint(i % 2))
				if err != nil {
					t.Fatal(err)
				}
				waiters = append(waiters, waiter)
			}
			_, err := queue.join("another-model")
			requireQueueFull(t, err, test.limit, test.limit)
			for _, waiter := range waiters {
				queue.leave(waiter)
				queue.leave(waiter)
			}
			queue.leave(nil)
			if queue.waiting != 0 || len(queue.queues) != 0 {
				t.Fatal("released waiter retained a queue slot")
			}
			if test.limit > 0 {
				waiter, err := queue.join("new-model")
				if err != nil {
					t.Fatal("released slot was not reusable", err)
				}
				queue.leave(waiter)
			}
		})
	}
}

func TestDispatchQueueConcurrentModelsAndWakeOrder(t *testing.T) {
	queue := newDispatchQueue()
	queue.setCapacity(10, 2)
	var running sync.WaitGroup
	accepted := make(chan *dispatchWaiter, 100)
	var rejected atomic.Int32
	for i := range 100 {
		running.Add(1)
		go func() {
			defer running.Done()
			waiter, err := queue.join(fmt.Sprint(i % 3))
			if err != nil {
				rejected.Add(1)
				return
			}
			accepted <- waiter
		}()
	}
	running.Wait()
	if len(accepted) != 4 || rejected.Load() != 96 || queue.waiting != 4 {
		t.Fatalf("accepted=%d rejected=%d waiting=%d", len(accepted), rejected.Load(), queue.waiting)
	}
	close(accepted)
	for waiter := range accepted {
		queue.leave(waiter)
	}
	first, _ := queue.join("same")
	second, _ := queue.join("same")
	<-first.wake
	queue.wakeHeads()
	queue.wakeHeads()
	if len(first.wake) != 1 || len(second.wake) != 0 || queue.waiting != 2 {
		t.Fatal("wake changed queue order or accounting")
	}
	queue.leave(first)
	if len(second.wake) != 1 {
		t.Fatal("leaving head did not wake its successor")
	}
	queue.leave(second)
}

type dispatchFixtureUpstream struct {
	aistudio.Service
	calls atomic.Int32
}

func (upstream *dispatchFixtureUpstream) Generate(context.Context, aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	upstream.calls.Add(1)
	return nil, errors.New("unexpected upstream generation")
}

// The fixture has only in-memory accounts and no browser, runtime lease or network transport.
func newDispatchFixture(t *testing.T, workers, concurrency int, mode string) (*trackedService, *dispatchFixtureUpstream) {
	t.Helper()
	account := &aistudio.Account{
		ID: "queue@example.invalid", Config: aistudio.DefaultAccountConfig("queue"), State: aistudio.AccountReady,
		Models: []aistudio.Model{
			{ID: "queue-model", Methods: []string{"generateContent"}, Capabilities: map[string]bool{"chat_model": true}},
			{ID: "other-model", Methods: []string{"generateContent"}, Capabilities: map[string]bool{"chat_model": true}},
		},
	}
	pool := aistudio.NewAccountPool([]*aistudio.Account{account}, concurrency)
	manager := &accountWorkerManager{
		pool: pool, dispatch: newDispatchQueue(), lifecycle: t.Context(),
		accounts: map[string]*accountWorker{account.ID: {id: account.ID}},
		openings: make(map[string]chan struct{}), victims: make(map[string]struct{}),
	}
	manager.setLimits(workers, workers, workers)
	switch mode {
	case "busy":
		for range concurrency {
			lease, _, err := pool.TryAcquireFor(t.Context(), aistudio.AccountSelection{AccountID: account.ID})
			if err != nil || lease == nil {
				t.Fatal("fixture could not occupy account", err)
			}
			t.Cleanup(func() { _ = lease.Release() })
		}
	case "opening":
		manager.openings[account.ID] = make(chan struct{})
	case "capacity":
		for i := range workers {
			manager.openings[fmt.Sprint(i)] = make(chan struct{})
		}
	case "cooling":
		if err := pool.MarkCooldownIfGeneration(account.ID, "", pool.ModelAccessGeneration(account.ID), time.Now(), time.Now().Add(30*time.Second), "synthetic cooldown"); err != nil {
			t.Fatal(err)
		}
	}
	upstream := &dispatchFixtureUpstream{}
	service := &trackedService{
		pool: pool, workers: manager, requests: newRequestRegistry(t.Context()), service: upstream,
		timeout: 5 * time.Second, lifecycle: t.Context(), dataContext: t.Context(),
	}
	service.state.Store(serviceRunning)
	return service, upstream
}

func queueRequest(id, model string) aistudio.GenerateRequest {
	return aistudio.GenerateRequest{ID: id, Model: model, Contents: []aistudio.Content{{Role: aistudio.RoleUser, Parts: []aistudio.Part{{Text: "hello"}}}}}
}

func TestGenerateQueueAdmissionAndCancellation(t *testing.T) {
	for _, mode := range []string{"busy", "opening", "capacity", "cooling"} {
		t.Run(mode, func(t *testing.T) {
			service, upstream := newDispatchFixture(t, 5, 1, mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			events, err := service.Generate(ctx, queueRequest("accepted", "queue-model"))
			if err != nil || events == nil {
				t.Fatal("queue admission waited for execution or failed", err)
			}
			service.workers.dispatch.wakeHeads()
			rejected, err := service.Generate(t.Context(), queueRequest("rejected", "other-model"))
			requireQueueFull(t, err, 1, 1)
			if rejected != nil || service.requests.count() != 1 || upstream.calls.Load() != 0 {
				t.Fatal("rejected request survived admission or called upstream")
			}
			cancel()
			for event := range events {
				if event.Kind != aistudio.EventError || !errors.Is(event.Err, context.Canceled) {
					t.Fatal("unexpected canceled request event", event)
				}
			}
			if service.workers.dispatch.waiting != 0 || service.requests.count() != 0 {
				t.Fatal("cancellation leaked queue or console state")
			}
			ctx, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer stop()
			events, err = service.Generate(ctx, queueRequest("timeout", "other-model"))
			if err != nil {
				t.Fatal("canceled slot was not reusable", err)
			}
			for event := range events {
				if !errors.Is(event.Err, context.DeadlineExceeded) {
					t.Fatal("unexpected timeout", event.Err)
				}
			}
			if service.workers.dispatch.waiting != 0 {
				t.Fatal("timeout leaked a queue slot")
			}
		})
	}
}

func TestGenerateZeroQueueAndHTTP429(t *testing.T) {
	service, upstream := newDispatchFixture(t, 2, 2, "busy")
	handler := api.NewHandler(service, api.Config{})
	for _, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":"queue-model","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if w.Code != 429 || !strings.Contains(w.Body.String(), "rate_limit_exceeded") || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
			t.Fatalf("queue overflow response=%d %s", w.Code, w.Body)
		}
	}
	if upstream.calls.Load() != 0 || service.requests.count() != 0 || service.workers.dispatch.waiting != 0 {
		t.Fatal("zero-queue rejection retained work")
	}
}

func TestGenerateConcurrentAdmissionSharesGlobalLimit(t *testing.T) {
	service, upstream := newDispatchFixture(t, 10, 2, "busy")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		events <-chan aistudio.Event
		err    error
	}
	results := make(chan outcome, 100)
	for i := range 100 {
		go func() {
			model := "queue-model"
			if i%2 == 0 {
				model = "other-model"
			}
			events, err := service.Generate(ctx, queueRequest(fmt.Sprint(i), model))
			results <- outcome{events, err}
		}()
	}
	var admitted []<-chan aistudio.Event
	for range 100 {
		result := <-results
		if result.err != nil {
			requireQueueFull(t, result.err, 4, 4)
		} else {
			admitted = append(admitted, result.events)
		}
	}
	if len(admitted) != 4 || service.requests.count() != 4 || upstream.calls.Load() != 0 {
		t.Fatalf("admitted=%d active=%d upstream=%d", len(admitted), service.requests.count(), upstream.calls.Load())
	}
	cancel()
	for _, events := range admitted {
		for range events {
		}
	}
	if service.workers.dispatch.waiting != 0 || service.requests.count() != 0 {
		t.Fatal("concurrent admission leaked waiters")
	}
}

func TestDispatchCapacityHotUpdatePreservesWaiters(t *testing.T) {
	service, _ := newDispatchFixture(t, 10, 2, "busy")
	service.state.Store(serviceStopped)
	queue := service.workers.dispatch
	var waiters []*dispatchWaiter
	for range 4 {
		waiter, err := queue.join("model")
		if err != nil {
			t.Fatal(err)
		}
		waiters = append(waiters, waiter)
	}
	service.ApplyCapacity(3, 3, 1, 2)
	_, err := queue.join("model")
	requireQueueFull(t, err, 4, 1)
	for _, waiter := range waiters[:3] {
		queue.leave(waiter)
	}
	_, err = queue.join("model")
	requireQueueFull(t, err, 1, 1)
	queue.leave(waiters[3])
	service.ApplyCapacity(10, 10, 1, 2)
	for range 4 {
		waiter, err := queue.join("model")
		if err != nil {
			t.Fatal("capacity increase did not take effect", err)
		}
		defer queue.leave(waiter)
	}
}
