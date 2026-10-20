package requestdb

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestManagementUsageSQLPageAndRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	store, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 0, 0, 0, 123000000, time.UTC)
	for _, row := range []Row{
		{ID: "req_a", Time: now, Account: "fixture-secret-label", Model: "gemini-3.8-flash", State: "completed", Status: 200, Duration: 2 * time.Second, Error: "fixture-secret-error"},
		{ID: "req_b", Time: now, Account: "fixture-secret-label", Model: "https://user:fixture-secret-password@example.invalid", State: "cancelled", Status: 499, Duration: time.Second},
		{ID: "req_c", Time: now.Add(-time.Second), Model: "gemini-3.8-flash", State: "failed", Status: 503},
	} {
		store.Record(row)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.ManagementUsage(context.Background(), contract.PageQuery{Offset: 0, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.Items[0].RequestID != "req_b" || page.Items[1].RequestID != "req_a" {
		t.Fatalf("unstable SQL page: %+v", page)
	}
	if page.Items[0].AccountID != nil || page.Items[0].Index != nil || page.Items[0].Model != nil || page.Items[0].Outcome != "aborted" {
		t.Fatal("ambiguous account/model leaked")
	}
	if page.Items[1].StartedAt.Time != now.Add(-2*time.Second) {
		t.Fatal("duration/time projection mismatch")
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), "error\":") || strings.Contains(string(raw), "accountName") {
		t.Fatalf("unsafe projection %s", raw)
	}
	empty, err := store.ManagementUsage(context.Background(), contract.PageQuery{Offset: 100, Limit: 2})
	if err != nil || empty.Total != 3 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty offset page err=%v", err)
	}
	if _, err := store.ManagementUsage(context.Background(), contract.PageQuery{Limit: 201}); err == nil {
		t.Fatal("unbounded page")
	}
}
