package camoufoxnative

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/logintotp"
)

// This function returns only fixed phase names. Credentials are arguments, never
// interpolated into expressions or returned in browser diagnostics.
const loginAutomationFunction = `function(action, value, email) {
  const outcome = (phase, reason) => action === 'diagnose' ? 'diag_' + reason : phase;
  const inspecting = action === 'inspect' || action === 'diagnose';
  try {
    const visible = e => !!e && !e.disabled && e.getAttribute('aria-disabled') !== 'true' &&
      !e.readOnly && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
    const unique = selector => { const es = [...document.querySelectorAll(selector)].filter(visible); return es.length === 1 ? es[0] : null; };
    const origin = location.origin;
    if (origin === 'https://myaccount.google.com') {
      const paths = ['/', '/interstitials/twosvrequired', '/signinoptions/two-step-verification/enroll-welcome', '/signinoptions/two-step-verification/enroll',
        '/signinoptions/twosv/enroll-welcome', '/signinoptions/twosv/enroll', '/signinoptions/two-step-verification',
        '/intro/signinoptions/two-step-verification'];
      const text = document.body?.innerText || '';
      if (!paths.includes(location.pathname) || !/Don't get locked out|2-Step Verification|两步验证|两步驟驗證/i.test(text)) return 'unknown';
      const labels = ['Do this later', '以后再说', '稍后再做', '稍后执行此操作'];
      const links = [...document.querySelectorAll('a,button,[role="button"],[role="link"]')]
        .filter(e => visible(e) && labels.includes((e.innerText || e.textContent || '').trim()));
      if (links.length !== 1) return 'manual';
      if (action === 'later') { links[0].click(); return 'submitted'; }
      return 'later';
    }
    if (origin !== 'https://accounts.google.com') return outcome('unknown', 'origin');
    if (!window.__aistudioLoginObserved && typeof document.addEventListener === 'function') {
      window.__aistudioLoginObserved = true;
      for (const name of ['input', 'keydown', 'pointerdown']) document.addEventListener(name, event => {
        if (event.isTrusted && event.target?.matches('input')) window.__aistudioLoginManual = true;
      }, true);
    }
    if (window.__aistudioLoginManual) return outcome('manual', 'user_input');
    const p = unique('input[type="password"]');
    const identifier = unique('input[type="email"],input[name="identifier"]');
    if ([...document.querySelectorAll('input[name="ca"],iframe[src*="recaptcha"],iframe[src*="captcha"]')].some(visible)) return outcome('manual', 'captcha');
    const identities = [...document.querySelectorAll('#profileIdentifier,[data-email],[data-identifier]')].filter(visible)
      .map(e => (e.getAttribute('data-email') || e.getAttribute('data-identifier') || e.textContent || '').trim().toLowerCase()).filter(v => v.includes('@'));
    if (identities.some(v => v !== email.toLowerCase())) return outcome('manual', 'account');
    const otpControls = [...document.querySelectorAll('#totpPin,input[name="totpPin"],input[autocomplete="one-time-code"]')].filter(visible);
    const otp = otpControls.length === 1 ? otpControls[0] : null;
    if (/\/challenge\/selection(?:\/|$)/.test(location.pathname)) {
      if (p || identifier || otpControls.length) return outcome('totp_loading', 'select_controls_loading');
      const shown = identities.length ? identities : ((document.body?.innerText || '').match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/ig) || []).map(v => v.toLowerCase());
      if (!shown.length) return outcome('totp_loading', 'select_identity_loading');
      if (!shown.includes(email.toLowerCase()) || shown.some(v => v !== email.toLowerCase())) return outcome('manual', 'account');
      const labels = ['Get a verification code from the Google Authenticator app',
        '从 Google 身份验证器应用获取验证码', '从“Google 身份验证器”应用获取验证码',
        '透過 Google Authenticator 應用程式取得驗證碼'];
      const choices = [...document.querySelectorAll('a,button,[role="link"],[role="button"]')]
        .filter(e => visible(e) && labels.includes((e.innerText || e.textContent || '').replace(/\s+/g, ' ').trim()));
      if (choices.length > 1) return outcome('manual', 'select_ambiguous');
      if (!choices.length) return outcome('totp_loading', 'select_missing');
      if (choices[0].__aistudioTotpSelected) return outcome('totp_loading', 'select_pending');
      if (inspecting) return outcome('totp_select', 'select_ready');
      if (action !== 'totp_select') return 'manual';
      choices[0].__aistudioTotpSelected = true;
      choices[0].click();
      return 'submitted';
    }
    const authenticator = /\/challenge\/totp(?:\/|$)/.test(location.pathname) || /Google Authenticator|Authenticator app|身份验证器|身分驗證器/i.test(document.body?.innerText || '');
    if (authenticator) {
      if (otpControls.length > 1) return outcome('manual', 'totp_controls');
      if (p || identifier) return outcome('totp_loading', 'totp_controls_loading');
      // Google renders the challenge, identity and enabled controls in separate
      // updates. An incomplete known TOTP page must not latch manual takeover.
      if (!otp) return outcome('totp_loading', 'totp_input_loading');
      const shown = identities.length ? identities : ((document.body?.innerText || '').match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/ig) || []).map(v => v.toLowerCase());
      if (shown.length === 0) return outcome('totp_loading', 'totp_identity_loading');
      if (!shown.includes(email.toLowerCase()) || shown.some(v => v !== email.toLowerCase())) return outcome('manual', 'account');
      if (otp.getAttribute('aria-invalid') === 'true') return outcome('manual', 'totp_invalid');
      if (otp.value) return inspecting && otp.__aistudioTotpSubmitted ? outcome('totp_pending', 'totp_pending') : outcome('manual', 'totp_prefilled');
      const nextControls = [...document.querySelectorAll('button#totpNext,#totpNext button,#totpNext[role="button"]')].filter(visible);
      if (nextControls.length > 1) return outcome('manual', 'totp_ambiguous');
      const next = nextControls[0];
      if (!next) return outcome('totp_loading', 'totp_next_loading');
      if (inspecting) return outcome('totp', 'totp_ready');
      if (action !== 'totp' || !/^\d{6}$/.test(value)) return 'manual';
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(otp, value);
      otp.dispatchEvent(new Event('input', {bubbles: true}));
      otp.dispatchEvent(new Event('change', {bubbles: true}));
      otp.__aistudioTotpSubmitted = true;
      next.click();
      return 'submitted';
    }
    const challenge = [...document.querySelectorAll('input[type="tel"],input[autocomplete="one-time-code"],input[name="totpPin"]')].some(visible);
    if (challenge) return outcome('manual', 'other_challenge');
    let phase = p ? 'password' : identifier ? 'email' : 'unknown';
    if (p?.getAttribute('aria-invalid') === 'true' || identifier?.getAttribute('aria-invalid') === 'true') return outcome('manual', 'credential_invalid');
    if (phase === 'unknown' && /\/challenge\//.test(location.pathname)) return outcome('manual', 'other_challenge');
    if (inspecting) return outcome(phase, phase);
    if (action !== phase) return 'manual';
    const input = phase === 'password' ? p : identifier;
    const next = unique(phase === 'password' ? '#passwordNext' : '#identifierNext');
    if (!input || !next || (input.value && input.value !== value)) return 'manual';
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, value);
    input.dispatchEvent(new Event('input', {bubbles: true}));
    input.dispatchEvent(new Event('change', {bubbles: true}));
    next.click();
    return 'submitted';
  } catch (_) { return outcome('manual', 'script_error'); }
}`

type loginAutomation struct {
	email, password string
	totpSecret      string
	now             func() time.Time
	view            *LiveView
	submitted       map[string]bool
	blocked         bool
	lastPhase       string
	phaseSince      time.Time
	acceptTerms     bool
	onboardingSince time.Time
	err             error
}

func newLoginAutomation(email, password string, view *LiveView, totpSecret ...string) *loginAutomation {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		email = ""
	}
	secret := ""
	if len(totpSecret) != 0 {
		secret = totpSecret[0]
	}
	return &loginAutomation{email: email, password: password, totpSecret: secret, now: time.Now, view: view, submitted: make(map[string]bool)}
}

func (automation *loginAutomation) String() string   { return "[login automation]" }
func (automation *loginAutomation) GoString() string { return "[login automation]" }

func (automation *loginAutomation) step(ctx context.Context, session *loginSession) {
	if ctx.Err() != nil {
		return
	}
	if automation.view != nil {
		automation.view.automationMu.Lock()
		defer automation.view.automationMu.Unlock()
		automation.view.mu.Lock()
		manual := automation.view.manualTakeover
		automation.view.mu.Unlock()
		if manual {
			return
		}
	}
	if automation.stepOnboarding(ctx, session) {
		return
	}
	phase := session.loginAction(ctx, "inspect", "", automation.email)
	if phase != automation.lastPhase {
		automation.lastPhase, automation.phaseSince = phase, time.Now()
		slog.Info("登录自动阶段", "phase", phase, "blocked", automation.blocked)
	}
	if phase == "manual" {
		automation.block(ctx, session, "page")
		return
	}
	if phase == "later" && !automation.submitted[phase] {
		automation.submitted[phase] = true
		if session.loginAction(ctx, phase, "", automation.email) != "submitted" {
			automation.view.setStatus("manual")
		}
		return
	}
	if automation.blocked {
		return
	}
	if phase == "totp_loading" {
		if time.Since(automation.phaseSince) > 10*time.Second {
			automation.block(ctx, session, "page")
		} else {
			automation.view.setStatus("totp_loading")
		}
		return
	}
	if phase == "totp_select" && !automation.submitted[phase] {
		if !logintotp.Valid(automation.totpSecret) {
			automation.block(ctx, session, "totp_key_unavailable")
			return
		}
		automation.submitted[phase] = true
		automation.view.setStatus("totp_loading")
		if session.loginAction(ctx, phase, "", automation.email) != "submitted" {
			automation.block(ctx, session, "page")
		}
		return
	}
	if phase == "totp" && !automation.submitted[phase] {
		if !logintotp.Valid(automation.totpSecret) {
			automation.block(ctx, session, "totp_key_unavailable")
			return
		}
		now := automation.now()
		if logintotp.Remaining(now) < 8*time.Second {
			automation.view.setStatus("totp_waiting")
			return // The normal 300ms loop continues to check cancel/takeover.
		}
		code, err := logintotp.Code(automation.totpSecret, now)
		if err != nil {
			automation.block(ctx, session, "totp_generation_failed")
			return
		}
		automation.submitted[phase] = true
		automation.view.setStatus("totp_filling")
		if session.loginAction(ctx, phase, code, automation.email) != "submitted" {
			automation.block(ctx, session, "page")
		}
		return
	}
	if phase != "email" && phase != "password" || automation.submitted[phase] {
		if time.Since(automation.phaseSince) > 10*time.Second {
			automation.block(ctx, session, "page")
		}
		return
	}
	value := automation.email
	if phase == "password" {
		value = automation.password
	}
	if value == "" {
		automation.block(ctx, session, "credential_unavailable")
		return
	}
	automation.submitted[phase] = true
	automation.view.setStatus("autofilling")
	if session.loginAction(ctx, phase, value, automation.email) != "submitted" {
		automation.block(ctx, session, "page")
	}
}

// block records only a fixed reason code; page text, URLs and credentials never
// enter diagnostics. Diagnose is read-only and cannot fill or click controls.
func (automation *loginAutomation) block(ctx context.Context, session *loginSession, reason string) {
	if !automation.blocked {
		if reason == "page" {
			reason = session.loginAction(ctx, "diagnose", "", automation.email)
		}
		slog.Info("登录自动流程需要手动处理", "reason", reason)
	}
	automation.blocked = true
	automation.view.setStatus("manual")
}

func (session *loginSession) loginAction(ctx context.Context, action, value, email string) string {
	arguments := []map[string]any{}
	for _, argument := range []string{action, value, email} {
		arguments = append(arguments, map[string]any{"type": "string", "value": argument})
	}
	result, err := session.client.command(ctx, "script.callFunction", map[string]any{
		"functionDeclaration": loginAutomationFunction, "arguments": arguments,
		"target": map[string]any{"context": session.contextID}, "awaitPromise": true,
	})
	// Do not propagate browser error payloads: they can echo function arguments.
	if err != nil || result["type"] == "exception" {
		return "manual"
	}
	remote, _ := result["result"].(map[string]any)
	phase, _ := remote["value"].(string)
	switch phase {
	case "diag_origin", "diag_user_input", "diag_captcha", "diag_account", "diag_select_controls_loading", "diag_totp_controls_loading",
		"diag_select_identity_loading", "diag_select_ambiguous", "diag_select_missing", "diag_select_pending", "diag_select_ready",
		"diag_totp_controls", "diag_totp_input_loading", "diag_totp_identity_loading", "diag_totp_invalid", "diag_totp_prefilled",
		"diag_totp_pending", "diag_totp_ambiguous", "diag_totp_next_loading", "diag_totp_ready", "diag_other_challenge",
		"diag_credential_invalid", "diag_email", "diag_password", "diag_unknown", "diag_script_error":
		if action == "diagnose" {
			return phase
		}
	case "email", "password", "totp", "totp_select", "totp_loading", "totp_pending", "later", "unknown", "submitted":
		return phase
	}
	return "manual"
}
