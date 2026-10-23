package host

import (
	"slices"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/core"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

type status struct {
	State           string                    `json:"state"`
	Enabled         bool                      `json:"enabled"`
	GenerationReady bool                      `json:"generation_ready"`
	Code            string                    `json:"code,omitempty"`
	ConfigRevision  uint64                    `json:"config_revision"`
	Transport       string                    `json:"transport"`
	Capabilities    map[string]string         `json:"capabilities"`
	AccountStates   []core.AccountState       `json:"account_states"`
	ModelStates     []modelStatus             `json:"model_states"`
	Capacity        capacityStatus            `json:"capacity"`
	Resources       resources.Snapshot        `json:"resources"`
	Conversations   core.ConversationSnapshot `json:"conversations"`
	LiveVerified    bool                      `json:"live_verified"`
}

type modelStatus struct {
	PublicID           string     `json:"public_id"`
	State              string     `json:"state"`
	Code               string     `json:"code,omitempty"`
	ConfigurationValid bool       `json:"configuration_valid"`
	Schedulable        bool       `json:"schedulable"`
	AuthVerified       bool       `json:"auth_verified"`
	ModelVerified      bool       `json:"model_verified"`
	AuthVerifiedAt     *time.Time `json:"auth_verified_at,omitempty"`
	ModelVerifiedAt    *time.Time `json:"model_verified_at,omitempty"`
	EligibleAccountIDs []string   `json:"eligible_account_ids"`
}

type capacityStatus struct {
	Limit     int `json:"limit"`
	Active    int `json:"active"`
	Available int `json:"available"`
}

type statusInput struct {
	Config                  config.Config
	Runtime                 core.RuntimeSnapshot
	EnginePresent           bool
	Generating              int
	Resources               resources.Snapshot
	BudgetPresent           bool
	Problem, AccountProblem string
	Closed                  bool
	OwnsLegacy              func(string) bool
}

// The caller holds h.mu. Snapshot copies only in-memory runtime state; this
// status endpoint must never load credentials, refresh auth or probe a model.
func (h *Host) statusLocked() status {
	input := statusInput{Config: h.config, EnginePresent: h.engine != nil, Generating: h.generating,
		Problem: h.problem, AccountProblem: h.accountProblem, Closed: h.closed.Load(), OwnsLegacy: h.ownsLegacy}
	if h.engine != nil {
		input.Runtime = h.engine.Snapshot()
	}
	if h.budget != nil {
		input.Resources, input.BudgetPresent = h.budget.Snapshot(), true
	}
	return computeStatus(input)
}

// computeStatus is deliberately pure. A configured mapping, an account that
// can accept work now, current authentication and successful model evidence
// are independent dimensions; none substitutes for another.
func computeStatus(input statusInput) status {
	c := input.Config
	s := status{State: "unavailable", Enabled: c.Enabled, ConfigRevision: c.Revision, Transport: "http",
		AccountStates: copyAccountStates(input.Runtime.Accounts), ModelStates: make([]modelStatus, 0, len(c.Bindings)),
		Capabilities: map[string]string{"text": "implemented", "streaming": "implemented", "generationConfig": "mapped_fields_forwarded_others_ignored", "multi_turn": "completed_prefix_only", "tools": "google_search_empty_config_provider_gated_others_ignored", "files": "implemented", "browser_collection": "remote_manual_capability_gated", "model_observation": "passive_pending_confirmation"}}
	s.Capabilities["inline_media"] = "bounded_passthrough_not_model_capability"
	s.Capabilities["model_import"] = "preview_apply_undo_disabled_models"
	s.Capabilities["countTokens"] = "unsupported_no_native_upstream_contract"
	s.Capabilities["usageMetadata"] = "unavailable_not_estimated"
	s.Capabilities["google_search"] = "provider_checked_empty_config_only"
	s.Capabilities["function_calling"] = "unsupported_ignored"
	s.Capabilities["code_execution"] = "request_control_unverified_output_passthrough"
	limit := max(0, c.MaxConcurrent)
	if input.EnginePresent {
		limit = min(limit, max(0, input.Runtime.Limit))
	}
	// Generating already includes requests holding an upstream lease. Adding
	// these counts would double-count work; parsing still reserves capacity.
	active := max(0, max(input.Runtime.Active, input.Generating))
	s.Capacity = capacityStatus{Limit: limit, Active: active, Available: max(0, limit-active)}
	s.Resources = input.Resources
	s.Conversations = input.Runtime.Conversations
	if !input.BudgetPresent {
		s.Resources = resources.Snapshot{LimitBytes: c.MaxInflightBytes, AvailableBytes: c.MaxInflightBytes}
	}
	blockedState, blockedCode := "", ""
	switch {
	case input.Closed:
		blockedState, blockedCode = "closed", "BUSINESS_CLOSED"
	case input.Problem != "":
		blockedState, blockedCode = "unavailable", input.Problem
	case input.AccountProblem != "":
		blockedState, blockedCode = "unavailable", input.AccountProblem
	case !c.Enabled:
		blockedState, blockedCode = "disabled", "BUSINESS_DISABLED"
	case input.EnginePresent && input.Runtime.Closed:
		blockedState, blockedCode = "unavailable", "BUSINESS_RUNTIME_STOPPED"
	case input.EnginePresent && input.Runtime.Suspended:
		// Suspension preserves current-credential auth/model evidence but is
		// never a schedulable runtime, including a transitional enabled view.
		blockedState, blockedCode = "unavailable", "BUSINESS_RUNTIME_SUSPENDED"
	case !input.EnginePresent:
		blockedState, blockedCode = "unavailable", "BUSINESS_ACCOUNT_REQUIRED"
	}
	if blockedState != "" {
		s.Capacity.Available = 0
	}
	publicCounts := make(map[string]int, len(c.Bindings))
	for _, binding := range c.Bindings {
		publicCounts[binding.PublicID]++
	}
	enabled, configured := 0, 0
	for _, binding := range c.Bindings {
		m := computeModelStatus(c, binding, input.Runtime.Accounts, input.OwnsLegacy)
		if publicCounts[binding.PublicID] > 1 {
			m.ConfigurationValid = false
			m.Schedulable = false
			m.State = "unavailable"
			m.Code = "BUSINESS_MODEL_CONFIGURATION_INVALID"
		}
		if binding.Enabled {
			enabled++
			if m.ConfigurationValid {
				configured++
			}
		}
		if m.Schedulable && s.Capacity.Available == 0 {
			m.Schedulable = false
			m.State = "unavailable"
			m.Code = "BUSINESS_CAPACITY_FULL"
		}
		if m.Schedulable && s.Resources.AvailableBytes < resources.GenerationReserveBytes {
			m.Schedulable, m.State, m.Code = false, "unavailable", "BUSINESS_RESOURCE_BUDGET_FULL"
		}
		if blockedState != "" {
			m.Schedulable = false
			m.State = "unavailable"
			m.Code = blockedCode
			if blockedState == "disabled" {
				m.State = "disabled"
			}
		}
		s.GenerationReady = s.GenerationReady || m.Schedulable
		s.ModelStates = append(s.ModelStates, m)
	}
	switch {
	case blockedState != "":
		s.State, s.Code = blockedState, blockedCode
		if blockedState == "disabled" {
			s.Code = ""
		}
	case s.GenerationReady:
		s.State = "ready"
	case enabled == 0:
		s.Code = "BUSINESS_MODEL_REQUIRED"
	case configured == 0:
		s.Code = "BUSINESS_MODEL_CONFIGURATION_INVALID"
	case len(input.Runtime.Accounts) == 0:
		s.Code = "BUSINESS_ACCOUNT_REQUIRED"
	default:
		// Preserve the specific model reason for the common single-model case;
		// multiple distinct account failures remain visible in model_states.
		for _, m := range s.ModelStates {
			if m.ConfigurationValid && m.State != "disabled" {
				if s.Code == "" {
					s.Code = m.Code
				} else if s.Code != m.Code {
					s.Code = "BUSINESS_ACCOUNT_UNAVAILABLE"
				}
			}
		}
		if s.Code == "" {
			s.Code = "BUSINESS_ACCOUNT_UNAVAILABLE"
		}
	}
	// A single observed account/model success is never whole-adapter live
	// certification. Global live_verified therefore remains false.
	return s
}

func computeModelStatus(c config.Config, binding config.Binding, accounts []core.AccountState, ownsLegacy func(string) bool) modelStatus {
	m := modelStatus{PublicID: binding.PublicID, State: "unavailable", EligibleAccountIDs: []string{}}
	// Validate this binding in isolation, as if enabled, so an unresolved or
	// unverified disabled mapping is not reported as usable configuration.
	check := c
	check.Bindings = []config.Binding{binding}
	check.Bindings[0].Enabled = true
	m.ConfigurationValid = binding.SelectorMode != "unresolved" && binding.VerificationStatus == "verified" && strings.TrimSpace(binding.Evidence) != "" && check.Validate(ownsLegacy) == nil
	modelKey := binding.UpstreamModelID
	if binding.SelectorMode == "auto" {
		modelKey = "auto"
	}
	matched, enabled := 0, 0
	states := map[string]bool{}
	for _, account := range accounts {
		if len(binding.AccountIDs) != 0 && !slices.Contains(binding.AccountIDs, account.ID) {
			continue
		}
		matched++
		if account.State == "disabled" {
			continue
		}
		enabled++
		states[account.State] = true
		// Evidence is scoped even when that account is busy or cooling down.
		// The timestamp can remain historical after JWT expiration or failure.
		m.AuthVerifiedAt = latestTime(m.AuthVerifiedAt, account.AuthVerifiedAt)
		if account.AuthVerified && account.State != "auth_required" {
			m.AuthVerified = true
		}
		if modelKey != "" {
			if at := account.VerifiedModels[modelKey]; !at.IsZero() {
				m.ModelVerified = true
				m.ModelVerifiedAt = latestTime(m.ModelVerifiedAt, at)
			}
		}
		// Busy, retired/draining and suspended accounts cannot take a lease.
		if account.State == "ready" && !slices.Contains(m.EligibleAccountIDs, account.ID) {
			m.EligibleAccountIDs = append(m.EligibleAccountIDs, account.ID)
		}
	}
	switch {
	case !binding.Enabled:
		m.State, m.Code = "disabled", "BUSINESS_MODEL_DISABLED"
	case !m.ConfigurationValid:
		m.Code = "BUSINESS_MODEL_CONFIGURATION_INVALID"
	case len(m.EligibleAccountIDs) > 0:
		m.State, m.Schedulable = "ready", true
	case matched == 0 && len(binding.AccountIDs) > 0:
		m.Code = "BUSINESS_ACCOUNT_SCOPE_EMPTY"
	case matched == 0:
		m.Code = "BUSINESS_ACCOUNT_REQUIRED"
	case enabled == 0:
		m.Code = "BUSINESS_ACCOUNT_DISABLED"
	case states["busy"]:
		m.Code = "BUSINESS_ACCOUNT_BUSY"
	case states["draining"]:
		m.Code = "BUSINESS_ACCOUNT_DRAINING"
	case states["cooldown"]:
		m.Code = "BUSINESS_ACCOUNT_COOLDOWN"
	case states["auth_required"]:
		m.Code = "BUSINESS_AUTH_REQUIRED"
	default:
		m.Code = "BUSINESS_ACCOUNT_UNAVAILABLE"
	}
	return m
}

func latestTime(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() || current != nil && !candidate.After(*current) {
		return current
	}
	value := candidate.UTC()
	return &value
}

func copyAccountStates(accounts []core.AccountState) []core.AccountState {
	result := make([]core.AccountState, 0, len(accounts))
	for _, account := range accounts {
		copy := account
		if account.VerifiedModels != nil {
			copy.VerifiedModels = make(map[string]time.Time, len(account.VerifiedModels))
			for model, at := range account.VerifiedModels {
				copy.VerifiedModels[model] = at
			}
		}
		result = append(result, copy)
	}
	return result
}
