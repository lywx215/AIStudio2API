package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type usageViewProbe struct {
	RequestLedger
	query UsageQuery
	calls int
}

func (probe *usageViewProbe) Usage(_ context.Context, query UsageQuery) (UsageReport, error) {
	probe.query = query
	probe.calls++
	return UsageReport{From: query.From, To: query.To, Groups: map[string][]UsageGroup{"account": {}, "status": {}}, Pairs: []UsagePair{}, Buckets: []UsageBucket{}, Series: []UsageSeries{}, Options: map[string][]string{"model": {}}}, nil
}
func TestUsageViewHTTPValidationAndShapes(t *testing.T) {
	probe := &usageViewProbe{}
	handler := NewHandler(nil, Config{Ledger: probe})
	base := "/api/usage?from=2026-10-19T00:00:00Z&to=2026-10-20T00:00:00Z"
	for _, test := range []struct {
		query  string
		field  string
		absent []string
	}{
		{"", "previous", nil}, {"&view=all", "pairs", nil},
		{"&view=overview", "previous", []string{"groups", "pairs", "series"}},
		{"&view=trends", "series", []string{"groups", "pairs", "previous", "cost", "recent"}},
		{"&view=dimensions&dimension=account", "groups", []string{"buckets", "pairs", "cost", "totals"}},
		{"&view=breakdown&dimension=model&key=", "pairs", []string{"buckets", "cost", "totals"}},
		{"&view=records-meta", "statuses", []string{"buckets", "cost", "totals", "groups"}},
	} {
		w := authExchange(handler, "GET", base+test.query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", test.query, w.Code, w.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body[test.field]; !ok {
			t.Fatalf("%s: missing %s", test.query, test.field)
		}
		for _, field := range test.absent {
			if _, ok := body[field]; ok {
				t.Fatalf("%s: computed field %s leaked", test.query, field)
			}
		}
	}
	for _, query := range []string{"&view=unknown", "&view=dimensions", "&view=dimensions&dimension=nope", "&view=dimensions&dimension=model&key=x", "&view=breakdown&dimension=status&key=200", "&view=breakdown&dimension=model", "&view=overview&dimension=model", "&view=trends&key=x"} {
		before := probe.calls
		w := authExchange(handler, "GET", base+query, "")
		if w.Code != 400 || before != probe.calls {
			t.Fatalf("invalid query reached ledger: %s %d", query, w.Code)
		}
	}
}
