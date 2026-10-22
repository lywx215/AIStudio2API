package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsOfflineAndDoesNotGuessModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business", "config.json")
	c, err := Load(path, nil)
	if err != nil || c.Enabled || len(c.Bindings) != 8 {
		t.Fatalf("invalid defaults: %v", err)
	}
	if err := c.Validate(nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range c.Bindings {
		if b.Enabled || b.UpstreamModelID != "" || b.VerificationStatus != "unverified" {
			t.Fatal("guessed mapping")
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("default load created storage")
	}
}

func TestBindingValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Config)
	}{
		{"unverified", func(c *Config) { c.Bindings[0].Enabled = true }},
		{"unresolved", func(c *Config) {
			b := &c.Bindings[1]
			b.Enabled = true
			b.VerificationStatus = "verified"
			b.Evidence = "observed"
		}},
		{"duplicate", func(c *Config) { c.Bindings[1].PublicID = c.Bindings[0].PublicID }},
		{"namespace", func(c *Config) { c.Bindings[0].PublicID = "gemini-default" }},
		{"auto-id", func(c *Config) { c.Bindings[0].UpstreamModelID = "guessed" }},
		{"exact-empty", func(c *Config) { c.Bindings[1].SelectorMode = "exact" }},
		{"too-many", func(c *Config) { c.MaxConcurrent = 999 }},
		{"invalid-version", func(c *Config) { c.SchemaVersion = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := Default()
			test.edit(&c)
			if c.Validate(nil) == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	c := Default()
	c.Bindings[0].Enabled = true
	c.Bindings[0].VerificationStatus = "verified"
	c.Bindings[0].Evidence = "synthetic observation"
	if err := c.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if c.Validate(func(string) bool { return true }) == nil {
		t.Fatal("accepted legacy alias collision")
	}
}

func TestStrictDecodeAndAtomicReplace(t *testing.T) {
	for _, body := range []string{`null`, `{} {}`, `{"unexpected":true}`, strings.Repeat(" ", MaxBytes+1)} {
		c, err := Decode(strings.NewReader(body))
		if err == nil && c.Validate(nil) == nil {
			t.Fatal("accepted invalid document")
		}
	}
	path := filepath.Join(t.TempDir(), "business", "config.json")
	c := Default()
	for rev := uint64(1); rev <= 2; rev++ {
		c.Revision = rev
		if err := Save(path, c); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path, nil)
		if err != nil || got.Revision != rev {
			t.Fatalf("readback=%d error=%v", got.Revision, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary settings file leaked")
	}
	data, _ := json.Marshal(c)
	if _, err := Decode(strings.NewReader(string(data) + "\ntrue")); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestObservedParameterCanUseDifferentUpstreamName(t *testing.T) {
	c := Default()
	c.Bindings[0].GenerationParameters = map[string]ParameterBinding{
		"maxOutputTokens": {UpstreamField: "observedTokenLimit", Verified: true, Evidence: "synthetic fixture"},
	}
	if err := c.Validate(nil); err != nil {
		t.Fatal("observed upstream name was restricted to native field spellings", err)
	}
	for _, field := range []string{"modelId", "nested.field", "", strings.Repeat("a", 65)} {
		c.Bindings[0].GenerationParameters["maxOutputTokens"] = ParameterBinding{UpstreamField: field, Verified: true, Evidence: "fixture"}
		if c.Validate(nil) == nil {
			t.Fatal("unsafe or reserved upstream name accepted", field)
		}
	}
}
