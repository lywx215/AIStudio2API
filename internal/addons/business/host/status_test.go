package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

const statusAccountA = "acct-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const statusAccountB = "acct-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const statusAccountC = "acct-cccccccccccccccccccccccccccccccc"

func statusFixture() statusInput {
	c := config.Default()
	c.Enabled, c.Revision = true, 9
	c.Bindings = []config.Binding{{PublicID: "business-fixture", DisplayName: "Fixture", SelectorMode: "exact", UpstreamModelID: "fixture-model", Enabled: true, VerificationStatus: "verified", Evidence: "synthetic mapping evidence", AccountIDs: []string{statusAccountA}}}
	return statusInput{Config: c, EnginePresent: true, Runtime: core.RuntimeSnapshot{Limit: 2, Accounts: []core.AccountState{{ID: statusAccountA, State: "ready"}}}}
}

func TestStatusReadinessDoesNotInventAuthOrModelEvidence(t *testing.T) {
	s := computeStatus(statusFixture())
	if s.State != "ready" || !s.GenerationReady || s.LiveVerified || s.ConfigRevision != 9 {
		t.Fatalf("incorrect top-level readiness: %+v", s)
	}
	m := s.ModelStates[0]
	if !m.ConfigurationValid || !m.Schedulable || m.AuthVerified || m.ModelVerified || m.AuthVerifiedAt != nil || m.ModelVerifiedAt != nil {
		t.Fatalf("mapping evidence became live proof: %+v", m)
	}
	if !reflect.DeepEqual(m.EligibleAccountIDs, []string{statusAccountA}) {
		t.Fatal(m.EligibleAccountIDs)
	}
	if s.Capabilities["multi_turn"] != "completed_prefix_only" || s.Capabilities["tools"] != "google_search_empty_config_provider_gated_others_ignored" || s.Capabilities["generationConfig"] != "mapped_fields_forwarded_others_ignored" {
		t.Fatal("capabilities were broadened", s.Capabilities)
	}
	for key, want := range map[string]string{
		"countTokens":      "unsupported_no_native_upstream_contract",
		"usageMetadata":    "unavailable_not_estimated",
		"google_search":    "provider_checked_empty_config_only",
		"function_calling": "unsupported_ignored",
		"code_execution":   "request_control_unverified_output_passthrough",
	} {
		if s.Capabilities[key] != want {
			t.Fatalf("capability %s = %q", key, s.Capabilities[key])
		}
	}
}

func TestStatusAccountScopesExcludeUnrelatedReadyAccounts(t *testing.T) {
	for _, test := range []struct {
		name, accountState, wantCode string
		exists                       bool
	}{
		{"missing", "", "BUSINESS_ACCOUNT_SCOPE_EMPTY", false},
		{"disabled", "disabled", "BUSINESS_ACCOUNT_DISABLED", true},
		{"busy", "busy", "BUSINESS_ACCOUNT_BUSY", true},
		{"draining", "draining", "BUSINESS_ACCOUNT_DRAINING", true},
		{"cooldown", "cooldown", "BUSINESS_ACCOUNT_COOLDOWN", true},
		{"auth_required", "auth_required", "BUSINESS_AUTH_REQUIRED", true},
		{"unknown", "unknown", "BUSINESS_ACCOUNT_UNAVAILABLE", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := statusFixture()
			input.Runtime.Accounts = []core.AccountState{{ID: statusAccountB, State: "ready", AuthVerified: true}}
			if test.exists {
				input.Runtime.Accounts = append(input.Runtime.Accounts, core.AccountState{ID: statusAccountA, State: test.accountState})
			}
			s := computeStatus(input)
			m := s.ModelStates[0]
			if s.GenerationReady || m.Schedulable || m.State != "unavailable" || m.Code != test.wantCode || len(m.EligibleAccountIDs) != 0 || m.AuthVerified {
				t.Fatalf("unrelated account affected readiness: %+v", s)
			}
		})
	}
}

func TestStatusPerModelScopeAndEvidenceAreIndependent(t *testing.T) {
	input := statusFixture()
	second := input.Config.Bindings[0]
	second.PublicID, second.SelectorMode, second.UpstreamModelID, second.AccountIDs = "business-auto", "auto", "", []string{statusAccountB}
	input.Config.Bindings = append(input.Config.Bindings, second)
	older := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	input.Runtime.Accounts = []core.AccountState{
		{ID: statusAccountA, State: "ready", AuthVerified: true, AuthVerifiedAt: older, VerifiedModels: map[string]time.Time{"auto": newer}},
		{ID: statusAccountB, State: "cooldown", AuthVerified: false, AuthVerifiedAt: newer, VerifiedModels: map[string]time.Time{"auto": older}},
		{ID: statusAccountC, State: "ready", AuthVerified: true, AuthVerifiedAt: newer.Add(time.Hour), VerifiedModels: map[string]time.Time{"fixture-model": newer.Add(time.Hour)}},
	}
	s := computeStatus(input)
	exact, auto := s.ModelStates[0], s.ModelStates[1]
	if !exact.Schedulable || !exact.AuthVerified || exact.ModelVerified || exact.AuthVerifiedAt == nil || !exact.AuthVerifiedAt.Equal(older) {
		t.Fatalf("wrong exact model proof: %+v", exact)
	}
	if auto.Schedulable || auto.AuthVerified || !auto.ModelVerified || auto.AuthVerifiedAt == nil || !auto.AuthVerifiedAt.Equal(newer) || auto.ModelVerifiedAt == nil || !auto.ModelVerifiedAt.Equal(older) {
		t.Fatalf("cooldown lost scoped evidence: %+v", auto)
	}
	if !s.GenerationReady {
		t.Fatal("one blocked model hid another schedulable model")
	}
}

func TestStatusCapacityIncludesParsingWithoutDoubleCountingLease(t *testing.T) {
	for _, test := range []struct {
		name                                                                           string
		hostLimit, runtimeLimit, active, parsing, wantLimit, wantActive, wantAvailable int
	}{
		{"same request counted once", 2, 2, 1, 1, 2, 1, 1},
		{"runtime full", 2, 2, 2, 0, 2, 2, 0},
		{"parsing full", 2, 2, 0, 2, 2, 2, 0},
		{"lower host limit", 1, 2, 0, 0, 1, 0, 1},
		{"lower runtime limit", 2, 1, 0, 1, 1, 1, 0},
		{"draining above limit", 1, 1, 2, 0, 1, 2, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := statusFixture()
			input.Config.MaxConcurrent = test.hostLimit
			input.Runtime.Limit, input.Runtime.Active, input.Generating = test.runtimeLimit, test.active, test.parsing
			s := computeStatus(input)
			if s.Capacity != (capacityStatus{Limit: test.wantLimit, Active: test.wantActive, Available: test.wantAvailable}) {
				t.Fatal(s.Capacity)
			}
			m := s.ModelStates[0]
			if m.Schedulable != (test.wantAvailable > 0) || s.GenerationReady != m.Schedulable {
				t.Fatalf("capacity ignored: %+v", s)
			}
			if test.wantAvailable == 0 && (m.Code != "BUSINESS_CAPACITY_FULL" || m.State != "unavailable") {
				t.Fatal(m)
			}
			if len(m.EligibleAccountIDs) != 1 {
				t.Fatal("global capacity erased locally eligible account IDs")
			}
		})
	}
}

func TestStatusGlobalBlockOverridesAllModels(t *testing.T) {
	for _, test := range []struct {
		name, state, code string
		change            func(*statusInput)
	}{
		{"addon disabled", "disabled", "BUSINESS_DISABLED", func(i *statusInput) { i.Config.Enabled = false }},
		{"host closed", "closed", "BUSINESS_CLOSED", func(i *statusInput) { i.Closed = true }},
		{"runtime closed", "unavailable", "BUSINESS_RUNTIME_STOPPED", func(i *statusInput) { i.Runtime.Closed = true }},
		{"runtime suspended", "unavailable", "BUSINESS_RUNTIME_SUSPENDED", func(i *statusInput) { i.Runtime.Suspended = true }},
		{"config error", "unavailable", "BUSINESS_CONFIG_INVALID", func(i *statusInput) { i.Problem = "BUSINESS_CONFIG_INVALID" }},
		{"vault error", "unavailable", "BUSINESS_VAULT_UNAVAILABLE", func(i *statusInput) { i.AccountProblem = "BUSINESS_VAULT_UNAVAILABLE" }},
		{"no runtime", "unavailable", "BUSINESS_ACCOUNT_REQUIRED", func(i *statusInput) { i.EnginePresent = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := statusFixture()
			test.change(&input)
			s := computeStatus(input)
			if s.State != test.state || s.GenerationReady || s.Capacity.Available != 0 {
				t.Fatal(s)
			}
			m := s.ModelStates[0]
			if !m.ConfigurationValid || m.Schedulable || m.Code != test.code {
				t.Fatal(m)
			}
			if test.state != "disabled" && s.Code != test.code {
				t.Fatal(s.Code)
			}
		})
	}
}

func TestStatusDisabledBindingsAndInvalidMappings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*statusInput)
		valid  bool
		state  string
	}{
		{"disabled configured", func(i *statusInput) { i.Config.Bindings[0].Enabled = false }, true, "disabled"},
		{"unresolved", func(i *statusInput) {
			i.Config.Bindings[0].SelectorMode = "unresolved"
			i.Config.Bindings[0].UpstreamModelID = ""
		}, false, "unavailable"},
		{"unverified", func(i *statusInput) { i.Config.Bindings[0].VerificationStatus = "unverified" }, false, "unavailable"},
		{"missing evidence", func(i *statusInput) { i.Config.Bindings[0].Evidence = "  " }, false, "unavailable"},
		{"legacy conflict", func(i *statusInput) { i.OwnsLegacy = func(id string) bool { return id == "business-fixture" } }, false, "unavailable"},
		{"malformed scope", func(i *statusInput) { i.Config.Bindings[0].AccountIDs = []string{"invalid"} }, false, "unavailable"},
		{"duplicate public ID", func(i *statusInput) { i.Config.Bindings = append(i.Config.Bindings, i.Config.Bindings[0]) }, false, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := statusFixture()
			test.change(&input)
			s := computeStatus(input)
			for _, m := range s.ModelStates {
				if m.ConfigurationValid != test.valid || m.Schedulable || m.State != test.state {
					t.Fatal(m)
				}
			}
			if s.GenerationReady {
				t.Fatal("invalid/disabled binding was ready")
			}
		})
	}
}

func TestStatusAuthRequiredCannotBeVerifiedButKeepsHistory(t *testing.T) {
	input := statusFixture()
	at := time.Date(2026, 10, 22, 2, 0, 0, 0, time.FixedZone("fixture", 8*60*60))
	input.Runtime.Accounts[0] = core.AccountState{ID: statusAccountA, State: "auth_required", AuthVerified: true, AuthVerifiedAt: at, VerifiedModels: map[string]time.Time{"fixture-model": at}}
	m := computeStatus(input).ModelStates[0]
	if m.AuthVerified || m.AuthVerifiedAt == nil || !m.AuthVerifiedAt.Equal(at) || m.AuthVerifiedAt.Location() != time.UTC || !m.ModelVerified || m.ModelVerifiedAt == nil {
		t.Fatal(m)
	}
	input.Runtime.Accounts[0].State = "disabled"
	m = computeStatus(input).ModelStates[0]
	if m.AuthVerified || m.ModelVerified || m.AuthVerifiedAt != nil || m.ModelVerifiedAt != nil {
		t.Fatal("disabled account contributed evidence", m)
	}
	input.Runtime.Accounts = nil
	m = computeStatus(input).ModelStates[0]
	if m.AuthVerified || m.ModelVerified || m.AuthVerifiedAt != nil || m.ModelVerifiedAt != nil {
		t.Fatal("deleted account contributed evidence", m)
	}
}

func TestStatusUnscopedBindingsUseOnlyCurrentRuntimeAccounts(t *testing.T) {
	input := statusFixture()
	input.Config.Bindings[0].AccountIDs = nil
	input.Runtime.Accounts = []core.AccountState{{ID: statusAccountA, State: "disabled"}, {ID: statusAccountB, State: "ready"}, {ID: statusAccountC, State: "busy"}}
	m := computeStatus(input).ModelStates[0]
	if !m.Schedulable || !reflect.DeepEqual(m.EligibleAccountIDs, []string{statusAccountB}) {
		t.Fatal(m)
	}
	input.Runtime.Accounts = nil
	if m = computeStatus(input).ModelStates[0]; m.Schedulable || m.Code != "BUSINESS_ACCOUNT_REQUIRED" {
		t.Fatal(m)
	}
}

func TestStatusJSONOmitsMissingEvidenceAndUsesArrays(t *testing.T) {
	input := statusFixture()
	input.Runtime.Accounts = nil
	s := computeStatus(input)
	data, err := json.Marshal(s.ModelStates[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "verified_at") || !strings.Contains(string(data), `"eligible_account_ids":[]`) {
		t.Fatal(string(data))
	}
	data, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"account_states":[]`) || !strings.Contains(string(data), `"live_verified":false`) || strings.Contains(string(data), "0001-") {
		t.Fatal(string(data))
	}
}

func TestStatusDoesNotMutateOrAliasSnapshots(t *testing.T) {
	input := statusFixture()
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	input.Runtime.Accounts[0].VerifiedModels = map[string]time.Time{"fixture-model": at}
	before, err := json.Marshal(input.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	s := computeStatus(input)
	s.AccountStates[0].VerifiedModels["fixture-model"] = at.Add(time.Hour)
	s.ModelStates[0].EligibleAccountIDs[0] = "changed"
	after, err := json.Marshal(input.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || input.Config.Bindings[0].AccountIDs[0] != statusAccountA {
		t.Fatal("status mutated its inputs")
	}
}

type statusNoNetworkTransport struct{ calls int }

func (t *statusNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("status must not access the network")
}

func TestStatusLockedUsesOnlyInMemoryRuntimeSnapshot(t *testing.T) {
	input := statusFixture()
	transport := &statusNoNetworkTransport{}
	engine := core.New(context.Background(), []vault.Account{{ID: statusAccountA, Enabled: true, Epoch: 1, Credential: vault.Credential{SecureCSES: "synthetic", CSESIndex: "synthetic", ConfigID: "synthetic"}}}, 2, time.Second, transport)
	defer engine.Close()
	// Deliberately omit all file/config/vault stores. Status must remain usable
	// with only an immutable configuration and an in-memory runtime snapshot.
	h := &Host{config: input.Config, engine: engine}
	h.mu.RLock()
	s := h.statusLocked()
	h.mu.RUnlock()
	if transport.calls != 0 || !s.GenerationReady || s.ModelStates[0].AuthVerified || s.ModelStates[0].ModelVerified {
		t.Fatalf("status acquired evidence: %+v, network=%d", s, transport.calls)
	}
}

func TestStatusSuspendedRuntimePreservesEvidenceButCannotSchedule(t *testing.T) {
	input := statusFixture()
	input.Runtime.Suspended = true
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	input.Runtime.Accounts[0] = core.AccountState{ID: statusAccountA, State: "suspended", AuthVerified: true, AuthVerifiedAt: at, VerifiedModels: map[string]time.Time{"fixture-model": at}}
	s := computeStatus(input)
	m := s.ModelStates[0]
	if s.State != "unavailable" || s.Code != "BUSINESS_RUNTIME_SUSPENDED" || s.GenerationReady || s.Capacity.Available != 0 || m.Schedulable || m.Code != "BUSINESS_RUNTIME_SUSPENDED" {
		t.Fatal(s)
	}
	if !m.ConfigurationValid || !m.AuthVerified || !m.ModelVerified || m.AuthVerifiedAt == nil || m.ModelVerifiedAt == nil || len(m.EligibleAccountIDs) != 0 {
		t.Fatal("suspension erased evidence or exposed a lease candidate", m)
	}
	input.Config.Enabled = false
	s = computeStatus(input)
	m = s.ModelStates[0]
	if s.State != "disabled" || m.State != "disabled" || m.Code != "BUSINESS_DISABLED" || m.Schedulable || !m.ConfigurationValid || !m.AuthVerified || !m.ModelVerified {
		t.Fatal("addon off did not override suspension independently of evidence", s)
	}
}
