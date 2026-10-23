package requestdb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

func TestUsageSourceMigrationPreservesCountsAndExportsProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema, ", usage_source TEXT NOT NULL DEFAULT '', usage_incomplete INTEGER NOT NULL DEFAULT 0", "", 1)
	if _, err = db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err = db.Exec(`INSERT INTO requests VALUES ('old',?,'gemini','/v1beta/models/old:generateContent','old','old','playground',200,'completed',1,0,0,11,0,7,18,0,'','[]',0)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	log := func(level, message string) { t.Errorf("%s: %s", level, message) }
	store, err := Open(path, log)
	if err != nil {
		t.Fatal(err)
	}
	store.Record(Row{ID: "new", Time: now.Add(time.Millisecond), Protocol: "gemini", Model: "business-test", Channel: "business", Status: 200, State: "completed", InputTokens: 20, ReasoningTokens: 3, ReplyTokens: 5, TotalTokens: 28, UsageSource: "local_gemma3_sentencepiece", UsageIncomplete: true})
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, log) // Migration must also be idempotent.
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	query := api.UsageRecordQuery{From: now.Add(-time.Second), To: now.Add(time.Second), Limit: 20}
	page, err := store.Records(context.Background(), query)
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	for _, record := range page.Items {
		if record.ID == "old" {
			if record.TotalTokens != 18 || record.InputTokens != 11 || record.ReplyTokens != 7 || record.UsageSource != "" || record.UsageIncomplete {
				t.Fatalf("old usage was relabeled or changed: %+v", record)
			}
		} else if record.TotalTokens != 28 || record.UsageSource != "local_gemma3_sentencepiece" || !record.UsageIncomplete || record.ReasoningTokens != 3 {
			t.Fatalf("local provenance lost after persistence: %+v", record)
		}
	}
	var output bytes.Buffer
	if err = store.ExportRecords(context.Background(), query, &output); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(output.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][len(rows[0])-2] != "usage_source" || rows[1][len(rows[1])-2] != "local_gemma3_sentencepiece" || rows[1][len(rows[1])-1] != "true" {
		t.Fatal(rows, err)
	}
}
