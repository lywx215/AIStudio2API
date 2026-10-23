package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestImportPoolProjectsOnlyDisabledNewBindings(t *testing.T) {
	current := Default()
	current.Revision, current.Enabled = 37, true
	before := current.Clone()
	source := []byte(`{"models":[{"id":"gemini-4-argon","name":"Do not overwrite","routing_mode":"explicit","upstream_model_id":"different"},{"id":"fixture","name":" Fixture model ","routing_mode":"explicit","upstream_model_id":"captured-model","enabled":true,"context_length":1000000,"max_tokens":null,"description":"not evidence"},{"id":"fixture-auto","name":"Auto","routing_mode":"auto"},{"id":"fixture-pending","name":"Pending","routing_mode":"unconfigured"},{"id":"legacy-model","name":"Legacy"}]}`)
	preview, err := PreviewImport(current, source)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Revision != 37 || len(preview.SourceHash) != 64 || len(preview.Added) != 4 || !reflect.DeepEqual(preview.Skipped, []string{"business-gemini-4-argon"}) {
		t.Fatal("incorrect preview", preview)
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatal("preview mutated current configuration")
	}
	if preview.Added[0].PublicID != "business-fixture" || preview.Added[0].DisplayName != "Fixture model" || preview.Added[0].UpstreamModelID != "captured-model" || preview.Added[1].SelectorMode != "auto" || preview.Added[2].SelectorMode != "unresolved" || preview.Added[3].UpstreamModelID != "legacy-model" {
		t.Fatal("reference routing contract changed", preview.Added)
	}
	for _, binding := range preview.Added {
		if binding.Enabled || binding.VerificationStatus != "unverified" || binding.Evidence != "" || len(binding.AccountIDs) != 0 {
			t.Fatal("import granted capability", binding)
		}
	}
	var value any
	_ = json.Unmarshal(source, &value)
	formatted, _ := json.MarshalIndent(value, "", "  ")
	// Object key order is significant to the source digest; insignificant
	// whitespace alone is not. A changed source always requires fresh preview.
	compact, _ := json.Marshal(value)
	a, _ := PreviewImport(current, formatted)
	b, _ := PreviewImport(current, compact)
	if a.SourceHash != b.SourceHash {
		t.Fatal("format-only changes changed source hash")
	}
	changed, _ := PreviewImport(current, []byte(strings.Replace(string(source), "captured-model", "other-model", 1)))
	if changed.SourceHash == preview.SourceHash {
		t.Fatal("changed source retained hash")
	}
}

func TestImportNativeClearsAllForeignTrustAndScope(t *testing.T) {
	preview, err := PreviewImport(Default(), []byte(`{"bindings":[{"public_id":"business-imported","display_name":"Imported","selector_mode":"exact","upstream_model_id":"captured-model","enabled":true,"verification_status":"verified","evidence":"PRIVATE-EVIDENCE","account_ids":["PRIVATE-ACCOUNT"],"generation_parameters":{"temperature":{"upstream_field":"samplingTemperature","verified":true,"evidence":"PRIVATE-PARAMETER"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b := preview.Added[0]
	if b.Enabled || b.VerificationStatus != "unverified" || b.Evidence != "" || len(b.AccountIDs) != 0 || b.GenerationParameters["temperature"].Verified || b.GenerationParameters["temperature"].Evidence != "" || b.GenerationParameters["temperature"].UpstreamField != "samplingTemperature" {
		t.Fatal("foreign trust retained", b)
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("preview returned discarded source values")
	}
	allSkipped, err := PreviewImport(Default(), []byte(`{"models":[{"id":"auto","name":"New auto","routing_mode":"auto"}]}`))
	if err != nil || len(allSkipped.Added) != 0 || len(allSkipped.Skipped) != 1 {
		t.Fatal("existing ID was not skipped", allSkipped, err)
	}
}

func TestImportRejectsMalformedSecretsDuplicatesAndLimits(t *testing.T) {
	valid := `{"models":[{"id":"fixture","name":"Fixture","routing_mode":"explicit","upstream_model_id":"model"}]}`
	cases := []string{
		`null`, `[]`, `{}`, `{"models":null}`, `{"models":[]}`, `{"models":{},"bindings":[]}`,
		`{"models":[],"accounts":[{"cookie":"PRIVATE-SECRET"}]}`,
		`{"models":[],"api_keys":["PRIVATE-SECRET"]}`,
		`{"models":[{"id":"fixture","name":"Fixture","cookie":"PRIVATE-SECRET"}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","routing_mode":null}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","enabled":null}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","upstream_model_id":null}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","routing_mode":"guess"}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","routing_mode":"explicit","upstream_model_id":""}]}`,
		`{"models":[{"id":"fixture","name":"Fixture","id":"other"}]}`,
		`{"models":[{"id":"fixture","name":"Fixture"},{"id":"business-fixture","name":"Duplicate"}]}`,
		`{"bindings":[{"public_id":"business-fixture","display_name":"Fixture","selector_mode":"exact","upstream_model_id":"model","generation_parameters":{"temperature":{"upstream_field":"x","Upstream_Field":"y"}}}]}`,
		strings.Replace(valid, "Fixture", `\ud800`, 1), strings.Replace(valid, "Fixture", string([]byte{0xff}), 1),
		valid + `{}`, strings.Repeat(" ", MaxBytes+1),
	}
	for i, source := range cases {
		if _, err := PreviewImport(Default(), []byte(source)); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("case %d: unsafe import accepted or leaked: %v", i, err)
		}
	}
	full := Default()
	for i := len(full.Bindings); i < 128; i++ {
		b := full.Bindings[0]
		b.PublicID = fmt.Sprintf("business-existing-%d", i)
		full.Bindings = append(full.Bindings, b)
	}
	// The limit is cumulative across existing and imported records.
	if _, err := PreviewImport(full, []byte(valid)); err == nil {
		t.Fatal("combined model limit bypassed")
	}
	deep := `{"models":` + strings.Repeat(`[`, 33) + `0` + strings.Repeat(`]`, 33) + `}`
	if ValidateImportJSON([]byte(deep)) == nil {
		t.Fatal("nesting limit bypassed")
	}
	if ValidateImportJSON([]byte(`{"text":"\ud83d\ude00\\ud800"}`)) != nil {
		t.Fatal("valid paired/literal Unicode rejected")
	}
}

func TestImportBoundsPersistedIndentedConfiguration(t *testing.T) {
	current := Default()
	current.Bindings = nil
	for i := 0; i < 128; i++ {
		b := Binding{PublicID: fmt.Sprintf("business-existing-%d", i), DisplayName: strings.Repeat("n", 160), SelectorMode: "auto", VerificationStatus: "unverified", Evidence: strings.Repeat("e", 1024)}
		current.Bindings = append(current.Bindings, b)
		if !ImportFitsStorage(current) {
			break
		}
	}
	compact, _ := json.Marshal(current)
	formatted, _ := json.MarshalIndent(current, "", "  ")
	if len(compact) >= MaxBytes || len(formatted)+1 <= MaxBytes {
		t.Fatal("fixture does not expose compact/persisted size difference", len(compact), len(formatted))
	}
	if _, err := PreviewImport(current, []byte(`{"models":[{"id":"new","name":"New"}]}`)); err == nil {
		t.Fatal("import wrote a configuration its loader would reject")
	}
}
