package camoufoxnative

import (
	"context"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/logintotp"
)

// This function returns only fixed phase names. Credentials are arguments, never
// interpolated into expressions or returned in browser diagnostics.
const loginAutomationFunction = `function(action, value, email) {
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
    if (origin !== 'https://accounts.google.com') return 'unknown';
    if (!window.__aistudioLoginObserved && typeof document.addEventListener === 'function') {
      window.__aistudioLoginObserved = true;
      for (const name of ['input', 'keydown', 'pointerdown']) document.addEventListener(name, event => {
        if (event.isTrusted && event.target?.matches('input')) window.__aistudioLoginManual = true;
      }, true);
    }
    if (window.__aistudioLoginManual) return 'manual';
    const p = unique('input[type="password"]');
    const identifier = unique('input[type="email"],input[name="identifier"]');
    if ([...document.querySelectorAll('input[name="ca"],iframe[src*="recaptcha"],iframe[src*="captcha"]')].some(visible)) return 'manual';
    const identities = [...document.querySelectorAll('#profileIdentifier,[data-email],[data-identifier]')].filter(visible)
      .map(e => (e.getAttribute('data-email') || e.getAttribute('data-identifier') || e.textContent || '').trim().toLowerCase()).filter(v => v.includes('@'));
    if (identities.some(v => v !== email.toLowerCase())) return 'manual';
    const otpControls = [...document.querySelectorAll('#totpPin,input[name="totpPin"],input[autocomplete="one-time-code"]')].filter(visible);
    const otp = otpControls.length === 1 ? otpControls[0] : null;
    const authenticator = /\/challenge\/totp(?:\/|$)/.test(location.pathname) || /Google Authenticator|Authenticator app|身份验证器|身分驗證器/i.test(document.body?.innerText || '');
    if (authenticator) {
      if (otpControls.length > 1 || p || identifier) return 'manual';
      // Google renders the challenge, identity and enabled controls in separate
      // updates. An incomplete known TOTP page must not latch manual takeover.
      if (!otp) return 'totp_loading';
      const shown = identities.length ? identities : ((document.body?.innerText || '').match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/ig) || []).map(v => v.toLowerCase());
      if (shown.length === 0) return 'totp_loading';
      if (!shown.includes(email.toLowerCase()) || shown.some(v => v !== email.toLowerCase()) || otp.getAttribute('aria-invalid') === 'true') return 'manual';
      if (otp.value) return action === 'inspect' && otp.__aistudioTotpSubmitted ? 'totp_pending' : 'manual';
      const nextControls = [...document.querySelectorAll('button#totpNext,#totpNext button,#totpNext[role="button"]')].filter(visible);
      if (nextControls.length > 1) return 'manual';
      const next = nextControls[0];
      if (!next) return 'totp_loading';
      if (action === 'inspect') return 'totp';
      if (action !== 'totp' || !/^\d{6}$/.test(value)) return 'manual';
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(otp, value);
      otp.dispatchEvent(new Event('input', {bubbles: true}));
      otp.dispatchEvent(new Event('change', {bubbles: true}));
      otp.__aistudioTotpSubmitted = true;
      next.click();
      return 'submitted';
    }
    const challenge = [...document.querySelectorAll('input[type="tel"],input[autocomplete="one-time-code"],input[name="totpPin"]')].some(visible);
    if (challenge) return 'manual';
    let phase = p ? 'password' : identifier ? 'email' : 'unknown';
    if (p?.getAttribute('aria-invalid') === 'true' || identifier?.getAttribute('aria-invalid') === 'true') return 'manual';
    if (phase === 'unknown' && /\/challenge\//.test(location.pathname)) return 'manual';
    if (action === 'inspect') return phase;
    if (action !== phase) return 'manual';
    const input = phase === 'password' ? p : identifier;
    const next = unique(phase === 'password' ? '#passwordNext' : '#identifierNext');
    if (!input || !next || (input.value && input.value !== value)) return 'manual';
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, value);
    input.dispatchEvent(new Event('input', {bubbles: true}));
    input.dispatchEvent(new Event('change', {bubbles: true}));
    next.click();
    return 'submitted';
  } catch (_) { return 'manual'; }
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
}

func newLoginAutomation(email, password string, view *LiveView, totpSecret ...string) *loginAutomation {
	if strings.TrimSpace(email) == "" {
		return nil
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
	phase := session.loginAction(ctx, "inspect", "", automation.email)
	if phase != automation.lastPhase {
		automation.lastPhase, automation.phaseSince = phase, time.Now()
	}
	if phase == "manual" {
		automation.blocked = true
		automation.view.setStatus("manual")
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
			automation.blocked = true
			automation.view.setStatus("manual")
		} else {
			automation.view.setStatus("totp_loading")
		}
		return
	}
	if phase == "totp" && !automation.submitted[phase] {
		if !logintotp.Valid(automation.totpSecret) {
			automation.blocked = true
			automation.view.setStatus("manual")
			return
		}
		now := automation.now()
		if logintotp.Remaining(now) < 8*time.Second {
			automation.view.setStatus("totp_waiting")
			return // The normal 300ms loop continues to check cancel/takeover.
		}
		code, err := logintotp.Code(automation.totpSecret, now)
		if err != nil {
			automation.blocked = true
			automation.view.setStatus("manual")
			return
		}
		automation.submitted[phase] = true
		automation.view.setStatus("totp_filling")
		if session.loginAction(ctx, phase, code, automation.email) != "submitted" {
			automation.blocked = true
			automation.view.setStatus("manual")
		}
		return
	}
	if phase != "email" && phase != "password" || automation.submitted[phase] {
		if time.Since(automation.phaseSince) > 10*time.Second {
			automation.blocked = true
			automation.view.setStatus("manual")
		}
		return
	}
	value := automation.email
	if phase == "password" {
		value = automation.password
	}
	if value == "" {
		automation.blocked = true
		automation.view.setStatus("manual")
		return
	}
	automation.submitted[phase] = true
	automation.view.setStatus("autofilling")
	if session.loginAction(ctx, phase, value, automation.email) != "submitted" {
		automation.blocked = true
		automation.view.setStatus("manual")
	}
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
	case "email", "password", "totp", "totp_loading", "totp_pending", "later", "unknown", "submitted":
		return phase
	}
	return "manual"
}
