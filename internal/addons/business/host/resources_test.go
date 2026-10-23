package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/gemini"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

type countedBody struct{ reads atomic.Int32 }

func (b *countedBody) Read([]byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (*countedBody) Close() error               { return nil }

func TestResourceAdmissionPrecedesBodyAndUpstream(t *testing.T) {
	var calls atomic.Int32
	h, _ := hotUpdateHost(t, offlineTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return fixtureResponse(500, "{}"), nil }))
	_, held := h.budget.Begin(context.Background())
	defer held.Close()
	if _, err := held.Reserve(h.config.MaxInflightBytes - 1); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		length int64
		code   int
	}{{-1, 429}, {gemini.MaxRequestBytes + 1, 413}} {
		body := &countedBody{}
		r := httptest.NewRequest("POST", "/v1beta/models/business-auto:generateContent", body)
		r.ContentLength = tc.length
		w := httptest.NewRecorder()
		h.PublicHandler().ServeHTTP(w, r)
		if w.Code != tc.code || body.reads.Load() != 0 || calls.Load() != 0 || h.generating != 0 {
			t.Fatalf("admission failed: code=%d reads=%d upstream=%d active=%d", w.Code, body.reads.Load(), calls.Load(), h.generating)
		}
	}
	held.Close()
	if h.budget.Snapshot().InUseBytes != 0 {
		t.Fatal("budget leak")
	}
}

func TestResourceBudgetHotDecreaseKeepsReservations(t *testing.T) {
	h, _ := hotUpdateHost(t, offlineTransport(func(*http.Request) (*http.Response, error) { return fixtureResponse(500, "{}"), nil }))
	engine := h.engine
	_, held := h.budget.Begin(context.Background())
	defer held.Close()
	if _, err := held.Reserve(96 << 20); err != nil {
		t.Fatal(err)
	}
	c := currentConfig(t, h)
	c.MaxInflightBytes = config.MinInflightBytes
	w := exchange(h.AdminHandler(), "PUT", "/api/addons/business/config", c)
	if w.Code != 200 || h.engine != engine {
		t.Fatal("budget save changed engine", w.Code)
	}
	s := h.budget.Snapshot()
	if s.LimitBytes != 64<<20 || s.InUseBytes != 96<<20 || s.AvailableBytes != 0 {
		t.Fatal(s)
	}
	if _, err := held.Reserve(1); err == nil {
		t.Fatal("admitted above lowered limit")
	}
	if h.statusLocked().GenerationReady {
		t.Fatal("over-budget host reported ready")
	}
	held.Close()
	if !h.statusLocked().GenerationReady || h.budget.Snapshot().InUseBytes != 0 {
		t.Fatal("capacity did not recover")
	}
}

func TestResourceBudgetReleasedAfterInvalidAndCancelledRequests(t *testing.T) {
	var calls atomic.Int32
	h, _ := hotUpdateHost(t, offlineTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return fixtureResponse(500, "{}"), nil }))
	for _, cancelled := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/v1beta/models/business-auto:generateContent", strings.NewReader("{"))
		ctx, cancel := context.WithCancel(r.Context())
		if cancelled {
			cancel()
		}
		w := httptest.NewRecorder()
		h.PublicHandler().ServeHTTP(w, r.WithContext(ctx))
		cancel()
		if w.Code < 400 || h.generating != 0 || h.budget.Snapshot().InUseBytes != 0 || calls.Load() != 0 {
			t.Fatal("invalid request leaked resources", w.Code)
		}
	}
}

func TestStatusResourceBudgetRequiresGenerationHeadroom(t *testing.T) {
	for _, available := range []int64{0, 1, resources.GenerationReserveBytes - 1, resources.GenerationReserveBytes} {
		input := statusFixture()
		input.BudgetPresent = true
		input.Resources = resources.Snapshot{LimitBytes: 64 << 20, AvailableBytes: available, InUseBytes: (64 << 20) - available}
		s := computeStatus(input)
		want := available >= resources.GenerationReserveBytes
		if s.GenerationReady != want || s.ModelStates[0].Schedulable != want || s.Resources != input.Resources {
			t.Fatal("budget readiness incorrect", s)
		}
		if !want && s.Code != "BUSINESS_RESOURCE_BUDGET_FULL" {
			t.Fatal(s.Code)
		}
	}
}
