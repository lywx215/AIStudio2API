package camoufoxnative

import (
	"context"
	"time"
)

// Only known first-use pages and uniquely labelled controls may be automated.
// The permission is supplied for this login, never inferred from the page.
const loginOnboardingFunction = `function(action, authorized, email) {
  const result = (phase, reason) => action === 'diagnose' ? 'diag_onboarding_' + reason : phase;
  const inspecting = action === 'inspect' || action === 'diagnose';
  try {
    if (location.origin !== 'https://aistudio.google.com') return 'not_onboarding';
    if (location.pathname !== '/onboarding' && location.pathname !== '/onboarding/') return 'aistudio_loading';
    if (!window.__aistudioOnboardingObserved && typeof document.addEventListener === 'function') {
      window.__aistudioOnboardingObserved = true;
      for (const name of ['input', 'keydown', 'pointerdown']) document.addEventListener(name, event => {
        if (event.isTrusted && !window.__aistudioOnboardingActing) window.__aistudioOnboardingManual = true;
      }, true);
    }
    if (window.__aistudioOnboardingManual) return result('manual', 'user_input');
    const visible = e => !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
    const enabled = e => !e.disabled && e.getAttribute('aria-disabled') !== 'true';
    const text = e => (e.innerText || e.textContent || '').replace(/\s+/g, ' ').trim();
    // Material icons contribute their ligature name to innerText in Firefox.
    const buttonText = e => text(e).replace(/\s+(?:arrow_forward|arrow_back|chevron_right|chevron_left)$/, '').trim();
    const click = e => {
      // Firefox dispatches trusted input/change events for checkbox.click().
      // Only suppress those synchronous events caused by our own activation.
      window.__aistudioOnboardingActing = true;
      try { e.click(); } finally { window.__aistudioOnboardingActing = false; }
    };
    const label = e => [e.getAttribute('aria-label') || '',
      ...[...(e.labels || [])].map(text),
      ...(e.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => text(document.getElementById(id) || {})),
      text(e.closest('label,mat-checkbox') || {})].join(' ').replace(/\s+/g, ' ').trim();
    const checked = e => e.checked === true || e.getAttribute('aria-checked') === 'true';
    const body = document.body?.innerText || '';
    const shown = [...document.querySelectorAll('[data-email],[data-identifier]')].filter(visible)
      .map(e => (e.getAttribute('data-email') || e.getAttribute('data-identifier') || '').trim().toLowerCase());
    if (email && shown.some(value => value.includes('@') && value !== email.toLowerCase())) return result('manual', 'account');
    const terms = /Welcome to Google AI Studio|欢迎使用 Google AI Studio|歡迎使用 Google AI Studio/i.test(body);
    const preferences = /What brings you to Google AI Studio\?|你为什么使用 Google AI Studio|您為什麼使用 Google AI Studio/i.test(body);
    if (terms && preferences) return result('onboarding_loading', 'transition');
    const buttons = [...document.querySelectorAll('button,[role="button"],a')].filter(visible);
    const state = window.__aistudioOnboardingState || (window.__aistudioOnboardingState = {});
    if (terms) {
      if (authorized !== true) return result('consent_required', 'authorization');
      if (state.termsSubmitted) return result('onboarding_pending', 'terms_pending');
      const boxes = [...document.querySelectorAll('input[type="checkbox"],[role="checkbox"]')].filter(visible);
      const required = boxes.filter(e => /at least\s*18|至少\s*18|年满\s*18|年滿\s*18/i.test(label(e)) && /developer|开发|開發/i.test(label(e)) && /professional|business|专业|商用|商业|專業|商業/i.test(label(e)));
      const marketing = boxes.filter(e => /receive emails|model updates|offers|订阅|邮件|訂閱|電子郵件/i.test(label(e)));
      const next = buttons.filter(e => ['Continue', '继续', '繼續'].includes(buttonText(e)));
      if (required.length > 1 || marketing.length > 1 || next.length > 1 || required.some(e => marketing.includes(e))) return result('manual', 'ambiguous');
      if (required.length !== 1 || marketing.length !== 1 || next.length !== 1 || !enabled(required[0]) || !enabled(marketing[0])) return result('onboarding_loading', 'controls_loading');
      if ((marketing.some(checked) && state.marketingChanged) || (!checked(required[0]) && state.requiredChanged)) return result('onboarding_loading', 'checkbox_pending');
      if (checked(required[0]) && !marketing.some(checked) && !enabled(next[0])) return result('onboarding_loading', 'continue_loading');
      if (inspecting) return result('onboarding_terms', 'terms_ready');
      if (action !== 'onboarding_terms') return result('manual', 'action');
      if (marketing.some(checked)) {
        state.marketingChanged = true;
        click(marketing[0]);
        return 'onboarding_loading';
      }
      if (!checked(required[0])) {
        state.requiredChanged = true;
        click(required[0]);
        return 'onboarding_loading';
      }
      if (!enabled(next[0])) return 'onboarding_loading';
      state.termsSubmitted = true;
      click(next[0]);
      return 'submitted';
    }
    if (preferences) {
      if (state.preferencesSubmitted) return result('onboarding_pending', 'preferences_pending');
      const skip = buttons.filter(e => ['Skip', '跳过', '略過', '跳過'].includes(buttonText(e)));
      if (skip.length > 1) return result('manual', 'ambiguous');
      if (skip.length !== 1 || !enabled(skip[0])) return result('onboarding_loading', 'skip_loading');
      if (inspecting) return result('onboarding_preferences', 'preferences_ready');
      if (action !== 'onboarding_preferences') return result('manual', 'action');
      state.preferencesSubmitted = true;
      click(skip[0]);
      return 'submitted';
    }
    return result('onboarding_loading', 'page_loading');
  } catch (_) { return result('manual', 'script_error'); }
}`

func (session *loginSession) onboardingAction(ctx context.Context, action string, authorized bool, email string) string {
	result, err := session.client.command(ctx, "script.callFunction", map[string]any{
		"functionDeclaration": loginOnboardingFunction,
		"arguments":           []map[string]any{{"type": "string", "value": action}, {"type": "boolean", "value": authorized}, {"type": "string", "value": email}},
		"target":              map[string]any{"context": session.contextID}, "awaitPromise": true,
	})
	if err != nil || result["type"] == "exception" {
		return "manual"
	}
	remote, _ := result["result"].(map[string]any)
	phase, _ := remote["value"].(string)
	switch phase {
	case "not_onboarding", "aistudio_loading", "onboarding_loading", "onboarding_pending", "consent_required", "onboarding_terms", "onboarding_preferences", "submitted", "manual":
		return phase
	case "diag_onboarding_user_input", "diag_onboarding_account", "diag_onboarding_transition", "diag_onboarding_authorization", "diag_onboarding_terms_pending", "diag_onboarding_ambiguous", "diag_onboarding_controls_loading", "diag_onboarding_checkbox_pending", "diag_onboarding_continue_loading", "diag_onboarding_terms_ready", "diag_onboarding_action", "diag_onboarding_preferences_pending", "diag_onboarding_skip_loading", "diag_onboarding_preferences_ready", "diag_onboarding_page_loading", "diag_onboarding_script_error":
		if action == "diagnose" {
			return phase
		}
	}
	return "manual"
}

func (automation *loginAutomation) stepOnboarding(ctx context.Context, session *loginSession) bool {
	phase := session.onboardingAction(ctx, "inspect", automation.acceptTerms, automation.email)
	if phase == "not_onboarding" {
		return false
	}
	if phase == "aistudio_loading" {
		automation.view.setStatus("verifying_identity")
		return true
	}
	if phase == "manual" || automation.blocked && automation.email != "" {
		automation.view.setStatus("manual")
		return true
	}
	if phase == "consent_required" {
		automation.view.setStatus("consent_required")
		return true
	}
	if automation.onboardingSince.IsZero() {
		automation.onboardingSince = time.Now()
	}
	if time.Since(automation.onboardingSince) > 60*time.Second {
		automation.err = &loginFlowError{code: "onboarding_timeout", message: "AI Studio 首次使用引导未在限定时间内完成"}
		return true
	}
	automation.view.setStatus(phase)
	if phase != "onboarding_terms" && phase != "onboarding_preferences" || automation.submitted[phase] {
		return true
	}
	result := session.onboardingAction(ctx, phase, automation.acceptTerms, automation.email)
	if result == "submitted" {
		automation.submitted[phase] = true
	} else if result != "onboarding_loading" && result != "onboarding_pending" && result != "consent_required" {
		automation.view.setStatus("manual")
	}
	return true
}
