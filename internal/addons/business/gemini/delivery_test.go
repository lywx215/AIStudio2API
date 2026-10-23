package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

type deliveryWriter struct {
	*httptest.ResponseRecorder
	failAt, writes int
}

func (w *deliveryWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("synthetic downstream disconnect")
	}
	return w.ResponseRecorder.Write(data)
}
func (w *deliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseRecorder }

func TestConversationCommitsOnlyAfterCompleteNativeDelivery(t *testing.T) {
	for _, tc := range []struct {
		action string
		failAt int
	}{
		{"generateContent", 1}, {"streamGenerateContent?alt=sse", 1}, {"streamGenerateContent?alt=json", 3},
	} {
		for _, failure := range []bool{false, true} {
			t.Run(tc.action+map[bool]string{true: "/disconnect", false: "/complete"}[failure], func(t *testing.T) {
				calls, delivered := 0, false
				g := generatorFunc(func(ctx context.Context, _ contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
					if !contract.OnDelivery(ctx, func(ok bool) { calls++; delivered = ok }) {
						t.Fatal("missing delivery contract")
					}
					if calls != 0 {
						t.Fatal("committed before response write")
					}
					return emit(contract.Event{Parts: []contract.Part{{Text: "complete"}}, Finished: true})
				})
				w := &deliveryWriter{ResponseRecorder: httptest.NewRecorder()}
				if failure {
					w.failAt = tc.failAt
				}
				Handler([]config.Binding{binding()}, g).ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:"+tc.action, strings.NewReader(prompt)))
				if calls != 1 || delivered == failure {
					t.Fatalf("callbacks=%d delivered=%v failure=%v writes=%d", calls, delivered, failure, w.writes)
				}
			})
		}
	}
}
