package observe

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalogScopeConflictExpiryAndCAS(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "observations.json"))
	now := time.Now()
	c.now = func() time.Time { return now }
	scope := Scope{"acct-test", 1, "config"}
	items, err := Parse(scope, Source{"model_config", "https://business.gemini.google/widget?token=secret"}, []byte(`{"configId":"config","uiSettings":{"modelConfigInfo":{"resolvedModels":[{"modelId":"real-42","displayName":"Model","token":"secret"}]}}}`), now)
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	s, err := c.Record(scope, items)
	if err != nil {
		t.Fatal(err)
	}
	if s.Entries[0].State != "pending" {
		t.Fatal(s)
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), "secret") {
		t.Fatal("non-selector field retained")
	}
	if _, err = c.Confirm(0, s.Entries[0].ID, scope); err != ErrConflict {
		t.Fatal("stale confirmation accepted", err)
	}
	s, err = c.Confirm(s.Revision, s.Entries[0].ID, scope)
	if err != nil || s.Entries[0].State != "confirmed" {
		t.Fatal(err)
	}
	items[0].UpstreamID = "other"
	s, err = c.Record(scope, items)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Entries {
		if e.State != "conflict" {
			t.Fatal(s)
		}
	}
	if _, err = c.Confirm(s.Revision, s.Entries[0].ID, scope); err == nil {
		t.Fatal("conflict confirmed")
	}
	now = now.Add(8 * 24 * time.Hour)
	s, err = c.List()
	if err != nil || s.Entries[0].State != "expired" {
		t.Fatal(err, s)
	}
	s, err = c.ReconcileScopes([]Scope{{"acct-test", 2, "config"}})
	if err != nil || s.Entries[0].State != "stale" {
		t.Fatal(err, s)
	}
}
func TestParserDoesNotGuessAutoOrModelLabels(t *testing.T) {
	scope := Scope{"acct", 1, "cfg"}
	source := Source{"user_request", "https://biz-discoveryengine.googleapis.com/v1alpha/widget:streamAssist"}
	items, err := Parse(scope, source, []byte(`{"streamAssistRequest":{},"prompt":"secret"}`), time.Now())
	if err != nil || len(items) != 1 || items[0].UpstreamID != "" || items[0].AutoForm != "generation_config_omitted" {
		t.Fatal(items, err)
	}
	c := New(filepath.Join(t.TempDir(), "o.json"))
	s, err := c.Record(scope, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Confirm(s.Revision, s.Entries[0].ID, scope); err == nil {
		t.Fatal("unresolved Auto confirmed")
	}
	for _, payload := range []string{`{"configId":"other","streamAssistRequest":{}}`, `{"streamAssistRequest":{"assistGenerationConfig":null}}`, strings.Repeat("x", (256<<10)+1)} {
		if _, err = Parse(scope, source, []byte(payload), time.Now()); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	items, err = Parse(scope, Source{"model_config", "https://business.gemini.google/widget"}, []byte(`{"uiSettings":{"modelConfigInfo":{"resolvedModels":[{"displayName":"Gemini 4 Argon","id":"guessed"}]}}}`), time.Now())
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
