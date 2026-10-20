package requestdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func usageFixture(t *testing.T, size int) (*Store, time.Time) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "usage.db"), func(level, message string) { t.Errorf("%s %s", level, message) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC().Truncate(time.Hour).Add(37 * time.Minute)
	store.now = func() time.Time { return now }
	models := []string{"gemini-4-argon", "gemini-3.8-flash", "gemini-2.5-pro", "unpriced", "gemini-3.1-pro-preview"}
	states := []string{"completed", "failed", "cancelled", "tool_calls", "max_tokens", "upstream_stop"}
	batch := make([]write, 0, 2048)
	for index := range size {
		input := int64(199999 + index%3) // Both sides of the long-context boundary.
		state, status := states[index%len(states)], 200
		if state == "failed" {
			status = 429
			if index%4 == 1 {
				status = 503
			}
		}
		row := &Row{ID: fmt.Sprintf("fixture-%06d", index), Time: now.Add(-time.Duration(index) * (89 * 24 * time.Hour / time.Duration(size))), Model: models[index%5], Account: fmt.Sprintf("account-%d", index%97), Channel: fmt.Sprintf("channel-%d", index%3), Protocol: fmt.Sprintf("protocol-%d", index%2), State: state, Status: status, InputTokens: input, CachedTokens: int64(index % 1000), ReasoningTokens: 50, ReplyTokens: 100, TotalTokens: input + 150, Duration: time.Duration(index%5000+100) * time.Millisecond, FirstEvent: time.Duration(index%200+1) * time.Millisecond, Queue: time.Duration(index%70) * time.Millisecond}
		if index%7 == 0 {
			row.FirstEvent = 0
		}
		batch = append(batch, write{row: row})
		if len(batch) == cap(batch) {
			if err := store.write(batch); err != nil {
				t.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if err := store.write(batch); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func TestUsageViewsMatchFrozenReport(t *testing.T) {
	store, now := usageFixture(t, 360)
	location, err := time.LoadLocation("Asia/Kathmandu")
	if err != nil {
		t.Fatal(err)
	}
	for _, filters := range []api.UsageFilters{nil, {"model": {"gemini-2.5-pro", "unpriced"}, "channel": {"channel-1"}}, {"account": {"missing"}}} {
		for _, span := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 90 * 24 * time.Hour} {
			bucket := time.Hour
			if span > 8*24*time.Hour {
				bucket = 24 * time.Hour
			}
			q := api.UsageQuery{From: now.Add(-span).Add(-13 * time.Second), To: now.Add(time.Second), Bucket: bucket, Location: location, Filters: filters, Stack: "model"}
			legacy, err := store.legacyUsage(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			full, err := store.Usage(context.Background(), q)
			if err != nil || !reflect.DeepEqual(full, legacy) {
				t.Fatalf("legacy report changed: %v", err)
			}
			q.View = "overview"
			overview, err := store.Usage(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(overview.ForView(q), legacy.ForView(q)) {
				t.Fatalf("overview differs for %v / %s", filters, span)
			}
			q.View = "trends"
			trends, err := store.Usage(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(trends.ForView(q), legacy.ForView(q)) {
				t.Fatalf("trends differ for %v / %s", filters, span)
			}
			for _, dimension := range usageDimensions {
				q.View, q.Dimension = "dimensions", dimension
				result, err := store.Usage(context.Background(), q)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result.ForView(q), legacy.ForView(q)) {
					t.Fatalf("dimension %s differs", dimension)
				}
			}
			q.View, q.Dimension = "records-meta", ""
			meta, err := store.Usage(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(meta.ForView(q), legacy.ForView(q)) {
				t.Fatal("records metadata differs")
			}
			for _, dimension := range []string{"account", "model"} {
				q.View, q.Dimension = "breakdown", dimension
				for _, key := range []string{"account-1", "gemini-2.5-pro", "missing", ""} {
					q.Key = key
					result, err := store.Usage(context.Background(), q)
					if err != nil {
						t.Fatal(err)
					}
					expected := []api.UsagePair{}
					for _, pair := range legacy.Pairs {
						if dimension == "account" && pair.Account == key || dimension == "model" && pair.Model == key {
							expected = append(expected, pair)
						}
					}
					if !reflect.DeepEqual(result.Pairs, expected) {
						t.Fatalf("breakdown differs: %s=%q", dimension, key)
					}
				}
			}
		}
	}
}

func TestRecordsMetaDoesNotDecodeHistograms(t *testing.T) {
	store, now := usageFixture(t, 20)
	if _, err := store.db.Exec(`UPDATE hours SET duration_hist = X'ff', first_event_hist = X'ff'; UPDATE days SET duration_hist = X'ff', first_event_hist = X'ff'`); err != nil {
		t.Fatal(err)
	}
	_, err := store.Usage(context.Background(), api.UsageQuery{View: "records-meta", From: now.Add(-7 * 24 * time.Hour), To: now, Bucket: time.Hour})
	if err != nil {
		t.Fatalf("metadata read a histogram: %v", err)
	}
}

func TestUsageEmptyViewsAndDSTBuckets(t *testing.T) {
	for _, size := range []int{0, 360} {
		store, now := usageFixture(t, size)
		location, err := time.LoadLocation("Australia/Sydney")
		if err != nil {
			t.Fatal(err)
		}
		q := api.UsageQuery{From: now.Add(-90 * 24 * time.Hour), To: now, Bucket: 24 * time.Hour, Location: location, Stack: "account"}
		before, err := store.legacyUsage(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range []string{"overview", "trends", "dimensions", "records-meta"} {
			q.View, q.Dimension = view, ""
			if view == "dimensions" {
				q.Dimension = "status"
			}
			after, err := store.Usage(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after.ForView(q), before.ForView(q)) {
				t.Fatalf("size=%d view=%s differs across empty/DST case", size, view)
			}
		}
	}
}

func TestUsageRecordMetadataMatchesCursorPages(t *testing.T) {
	store, now := usageFixture(t, 120)
	q := api.UsageQuery{View: "records-meta", From: now.Add(-90 * 24 * time.Hour), To: now.Add(time.Second), Filters: api.UsageFilters{"channel": {"channel-1"}}}
	meta, err := store.Usage(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	recordQuery := api.UsageRecordQuery{From: q.From, To: q.To, Filters: q.Filters, Limit: 20}
	seen := map[string]bool{}
	for {
		page, err := store.Records(context.Background(), recordQuery)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate page record: %s", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		last := page.Items[len(page.Items)-1]
		recordQuery.Before = &api.UsageCursor{Time: last.Time, ID: last.ID}
	}
	if int64(len(seen)) != meta.Totals.Requests {
		t.Fatalf("metadata=%d records=%d", meta.Totals.Requests, len(seen))
	}
}

// Opt-in, repeatable before/after timing using the same database and frozen oracle.
func TestUsagePerformance(t *testing.T) {
	if os.Getenv("USAGE_PERF") != "1" {
		t.Skip("set USAGE_PERF=1 for the 100,000-record comparison")
	}
	store, now := usageFixture(t, 100000)
	ctx := context.Background()
	var results []map[string]any
	for _, days := range []int{1, 7, 90} {
		bucket := time.Hour
		if days > 8 {
			bucket = 24 * time.Hour
		}
		q := api.UsageQuery{From: now.Add(-time.Duration(days) * 24 * time.Hour), To: now, Bucket: bucket, Location: time.UTC, Stack: "model"}
		for _, name := range []string{"before", "overview", "trends", "dimensions", "records-meta"} {
			q.View, q.Dimension = name, ""
			if name == "before" {
				q.View = ""
			}
			if name == "dimensions" {
				q.Dimension = "account"
			}
			if directory := os.Getenv("USAGE_PERF_OUT"); directory != "" && name == "before" {
				report, err := store.legacyUsage(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("baseline-%dd.json", days)), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			run := func() (int, error) {
				var report api.UsageReport
				var err error
				if name == "before" {
					report, err = store.legacyUsage(ctx, q)
				} else {
					report, err = store.Usage(ctx, q)
				}
				if err != nil {
					return 0, err
				}
				body, err := json.Marshal(report.ForView(q))
				return len(body), err
			}
			for range 3 {
				if _, err := run(); err != nil {
					t.Fatal(err)
				}
			}
			times := make([]float64, 20)
			var bytes, allocations uint64
			var response int
			for index := range times {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				started := time.Now()
				var err error
				response, err = run()
				times[index] = float64(time.Since(started).Microseconds()) / 1000
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				bytes += after.TotalAlloc - before.TotalAlloc
				allocations += after.Mallocs - before.Mallocs
			}
			slices.Sort(times)
			results = append(results, map[string]any{"days": days, "view": name, "median_ms": (times[9] + times[10]) / 2, "p95_ms": times[18], "response_bytes": response, "alloc_bytes": bytes / 20, "allocs": allocations / 20})
			t.Logf("PERF days=%d view=%s median_ms=%.3f p95_ms=%.3f response_bytes=%d alloc_bytes=%d allocs=%d", days, name, (times[9]+times[10])/2, times[int(math.Ceil(float64(len(times))*0.95))-1], response, bytes/20, allocations/20)
		}
	}
	if directory := os.Getenv("USAGE_PERF_OUT"); directory != "" {
		body, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "performance.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
