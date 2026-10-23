package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/gemini"
)

type businessUsageGenerator struct{ fail bool }

func (g businessUsageGenerator) Generate(ctx context.Context, selection contract.Selection, _ contract.Request, emit func(contract.Event) error) error {
	contract.ReportTarget(ctx, selection.PublicID, "synthetic-business-account")
	contract.ReportUpstream(ctx)
	if err := emit(contract.Event{Parts: []contract.Part{{Text: "synthetic reply"}}}); err != nil {
		return err
	}
	if g.fail {
		return contract.Fail(502, "UNAVAILABLE", "Synthetic upstream interrupted")
	}
	return emit(contract.Event{Finished: true})
}

func TestBusinessUsageRecordedOnceAndAuthenticationPreserved(t *testing.T) {
	for _, fail := range []bool{false, true} {
		probe := &upstreamLogProbe{}
		cfg := Config{APIKey: "synthetic-key", Admin: probe, Business: &BusinessAddon{Public: gemini.Handler([]config.Binding{{PublicID: "business-test", Enabled: true}}, businessUsageGenerator{fail})}}
		handler := attachBusiness(http.NotFoundHandler(), cfg, newAdminAuth(cfg))
		for _, key := range []string{"", "synthetic-key"} {
			request := httptest.NewRequest("POST", "/v1beta/models/business-test:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
			request.Header.Set("x-goog-api-key", key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request)
			if key == "" {
				if w.Code != 401 || len(probe.ends) != 0 {
					t.Fatal("unauthorized Business request counted or logged", w.Code, probe.ends)
				}
				continue
			}
			if len(probe.starts) != 1 || len(probe.ends) != 1 {
				t.Fatal("missing or duplicate request records", len(probe.starts), len(probe.ends))
			}
			entry := probe.ends[0]
			if !entry.Authorized || !entry.UpstreamAttempted || entry.Model != "business-test" || entry.Account != "synthetic-business-account" || entry.Channel != "business" {
				t.Fatalf("lost Business access identity: %+v", entry)
			}
			if fail {
				if entry.Status != 502 || entry.Usage != nil || entry.Error == "" || entry.FinishReason != "" {
					t.Fatalf("interrupted stream logged as success: %+v", entry)
				}
				continue
			}
			var terminal struct {
				Usage contract.Usage `json:"usageMetadata"`
			}
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, "usageMetadata") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &terminal); err != nil {
						t.Fatal(err)
					}
				}
			}
			if entry.Status != 200 || entry.Usage == nil || entry.Usage.Source != contract.LocalUsageSource || entry.Usage.TotalTokens != terminal.Usage.TotalTokenCount || entry.Usage.TotalTokens < 1 || entry.FinishReason != "stop" {
				t.Fatalf("ledger/client usage diverged: %+v, %+v", entry, terminal)
			}
		}
	}
}
