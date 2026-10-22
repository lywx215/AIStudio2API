package camoufoxnative

import (
	"context"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/waa/goja"
)

func TestTotpDOMGuards(t *testing.T) {
	for _, mode := range []string{"valid", "foreign", "http", "wrong_email", "no_email", "sms", "ambiguous", "ambiguous_next", "disabled", "prefilled", "invalid", "captcha", "manual", "origin_changed", "loading", "next_loading", "identity_loading", "input_loading", "selection", "selection_wrong_email", "selection_no_email", "selection_ambiguous", "selection_missing", "selection_disabled"} {
		t.Run(mode, func(t *testing.T) {
			vm := goja.New()
			_ = vm.Set("mode", mode)
			_, err := vm.RunString(`
var clicked=0, filled='', window={};
function HTMLInputElement() {}
Object.defineProperty(HTMLInputElement.prototype,'value',{set:function(v){this._value=v;filled=v},get:function(){return this._value||''}});
function Event() {}
const props={getClientRects:()=>[1],getAttribute:()=>null,dispatchEvent:()=>{}};
const otp=Object.assign(new HTMLInputElement(),props);
if(mode==='input_loading')otp.disabled=true;
if(mode==='prefilled')otp._value='123456';
if(mode==='invalid')otp.getAttribute=n=>n==='aria-invalid'?'true':null;
const next=Object.assign({click:()=>{clicked++},disabled:mode==='disabled'||mode==='next_loading'},props);
const identity=Object.assign({textContent:mode==='wrong_email'||mode==='selection_wrong_email'?'other@example.test':'user@example.test'},props);
const choice=Object.assign({textContent:'Get a verification code from the Google Authenticator app',click:()=>{clicked++},disabled:mode==='selection_disabled'},props);
const location={origin:mode==='foreign'?'https://accounts.google.com.evil.test':mode==='http'?'http://accounts.google.com':'https://accounts.google.com',pathname:mode.startsWith('selection')?'/v3/signin/challenge/selection':mode==='sms'?'/signin/challenge/sms':'/v3/signin/challenge/totp'};
const getComputedStyle=()=>({visibility:'visible'});
if(mode==='manual')window.__aistudioLoginManual=true;
const document={body:{innerText:mode==='sms'?'Enter SMS code':'Get a verification code from the Google Authenticator app'},querySelectorAll:s=>{
 if(s.startsWith('#profileIdentifier'))return mode==='no_email'||mode==='identity_loading'||mode==='selection_no_email'?[]:[identity];
 if(s.startsWith('#totpPin'))return mode==='loading'||mode.startsWith('selection')?[]:mode==='ambiguous'?[otp,otp]:[otp];
 if(s.includes('#totpNext'))return mode==='ambiguous_next'?[next,next]:[next];
 if(s.startsWith('a,button'))return mode==='selection_missing'?[]:mode==='selection_ambiguous'?[choice,choice]:[choice];
 if(s.startsWith('input[name="ca"]'))return mode==='captcha'?[otp]:[];
 if(s.startsWith('input[type="tel"]'))return [otp];
 return [];
}};
`)
			if err != nil {
				t.Fatal(err)
			}
			value, err := vm.RunString("(" + loginAutomationFunction + ")")
			if err != nil {
				t.Fatal(err)
			}
			fn, _ := goja.AssertFunction(value)
			invoke := func(action string) string {
				v, err := fn(goja.Undefined(), vm.ToValue(action), vm.ToValue("005924"), vm.ToValue("user@example.test"))
				if err != nil {
					t.Fatal(err)
				}
				return v.String()
			}
			phase := invoke("inspect")
			if (mode == "loading" || mode == "next_loading" || mode == "identity_loading" || mode == "input_loading") && phase != "totp_loading" {
				t.Fatalf("transient Google page should wait; got %s", phase)
			}
			if mode == "origin_changed" {
				if phase != "totp" {
					t.Fatal("fixture not ready")
				}
				_, _ = vm.RunString(`location.origin='https://example.test'`)
			}
			result := invoke("totp")
			if mode == "valid" {
				if phase != "totp" || result != "submitted" || vm.Get("filled").String() != "005924" || vm.Get("clicked").ToInteger() != 1 {
					t.Fatal("TOTP submission failed")
				}
				if invoke("inspect") != "totp_pending" {
					t.Fatal("own submitted value mistaken for manual input")
				}
			} else if result == "submitted" || vm.Get("clicked").ToInteger() != 0 || vm.Get("filled").String() != "" {
				t.Fatal("unsafe challenge submitted")
			}
			if mode == "selection" {
				if phase != "totp_select" || invoke("totp_select") != "submitted" || vm.Get("clicked").ToInteger() != 1 || vm.Get("filled").String() != "" {
					t.Fatal("explicit Authenticator choice not selected")
				}
				if invoke("totp_select") == "submitted" || vm.Get("clicked").ToInteger() != 1 {
					t.Fatal("Authenticator choice clicked repeatedly")
				}
			} else if mode != "valid" && invoke("totp_select") == "submitted" {
				t.Fatal("unsafe Authenticator choice selected")
			}
		})
	}
}

func TestTotpSelectionRequiresSecretAndSubmitsOnce(t *testing.T) {
	for _, secret := range []string{"", "invalid!", "JBSWY3DPEHPK3PXP"} {
		session, actions := automationFixture(t, []string{"totp_select", "totp_select", "totp"})
		view := NewLiveView()
		automation := newLoginAutomation("user@example.test", "synthetic", view, secret)
		automation.now = func() time.Time { return time.Unix(30, 0) }
		for range 3 {
			automation.step(context.Background(), session)
		}
		if secret == "JBSWY3DPEHPK3PXP" {
			if automation.blocked || len(actions) != 2 || <-actions != "totp_select" || <-actions != "totp" {
				t.Fatal("selection and TOTP not submitted exactly once")
			}
		} else if len(actions) != 0 || view.status != "manual" {
			t.Fatal("Authenticator choice selected without a usable secret")
		}
	}
}

func TestTotpLoadingDoesNotPermanentlyBlockAutofill(t *testing.T) {
	session, actions := automationFixture(t, []string{"totp_loading", "totp_loading", "totp", "totp_pending"})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "synthetic", view, "JBSWY3DPEHPK3PXP")
	automation.now = func() time.Time { return time.Unix(30, 0) }
	for range 4 {
		automation.step(context.Background(), session)
	}
	if automation.blocked || len(actions) != 1 || <-actions != "totp" {
		t.Fatal("partially loaded TOTP page permanently disabled autofill")
	}
}

func TestTotpLoadingStillTimesOutAndAllowsTakeover(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		session, actions := automationFixture(t, []string{"totp_loading", "totp_loading", "totp"})
		view := NewLiveView()
		automation := newLoginAutomation("user@example.test", "synthetic", view, "JBSWY3DPEHPK3PXP")
		automation.step(context.Background(), session)
		if view.status != "totp_loading" {
			t.Fatal("loading status not exposed")
		}
		if takeover {
			view.takeOver()
		} else {
			automation.phaseSince = time.Now().Add(-11 * time.Second)
		}
		automation.step(context.Background(), session)
		automation.step(context.Background(), session)
		if len(actions) != 0 || view.status != "manual" {
			t.Fatal("loading wait ignored timeout or takeover")
		}
	}
}

func TestTotpWaitBoundarySubmitOnceAndTakeover(t *testing.T) {
	session, actions := automationFixture(t, []string{"totp", "totp", "totp", "totp", "manual", "totp", "later"})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "synthetic", view, "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	now := time.Unix(29, 0)
	automation.now = func() time.Time { return now }
	automation.step(context.Background(), session)
	if len(actions) != 0 || view.status != "totp_waiting" {
		t.Fatal("nearly expired code submitted")
	}
	now = time.Unix(30, 0)
	automation.step(context.Background(), session)
	automation.step(context.Background(), session)
	if len(actions) != 1 || <-actions != "totp" {
		t.Fatal("code not submitted exactly once")
	}
	view.takeOver()
	automation.step(context.Background(), session)
	if len(actions) != 0 {
		t.Fatal("takeover allowed further action")
	}
	waitSession, waitActions := automationFixture(t, []string{"totp", "totp"})
	waitView := NewLiveView()
	waitAutomation := newLoginAutomation("user@example.test", "synthetic", waitView, "JBSWY3DPEHPK3PXP")
	now = time.Unix(29, 0)
	waitAutomation.now = func() time.Time { return now }
	waitAutomation.step(context.Background(), waitSession)
	waitView.takeOver()
	now = time.Unix(30, 0)
	waitAutomation.step(context.Background(), waitSession)
	if len(waitActions) != 0 || waitView.status != "manual" {
		t.Fatal("takeover during wait did not stop submission")
	}
}

func TestTotpMissingInvalidCancelledAndRejected(t *testing.T) {
	for _, secret := range []string{"", "not valid!"} {
		session, actions := automationFixture(t, []string{"totp"})
		view := NewLiveView()
		automation := newLoginAutomation("user@example.test", "synthetic", view, secret)
		automation.step(context.Background(), session)
		if len(actions) != 0 || view.status != "manual" {
			t.Fatal("invalid key did not hand off")
		}
	}
	session, actions := automationFixture(t, []string{"totp", "manual", "totp"})
	view := NewLiveView()
	automation := newLoginAutomation("user@example.test", "synthetic", view, "JBSWY3DPEHPK3PXP")
	automation.now = func() time.Time { return time.Unix(30, 0) }
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	automation.step(cancelled, session)
	if len(actions) != 0 {
		t.Fatal("cancelled login submitted code")
	}
	for range 3 {
		automation.step(context.Background(), session)
	}
	if len(actions) != 1 || view.status != "manual" {
		t.Fatal("rejected code was retried")
	}
}
