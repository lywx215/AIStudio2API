package camoufoxnative

import (
	"github.com/Mag1cFall/AIStudio2API/internal/waa/goja"
	"testing"
)

func TestLoginAutomationPageGuardsAndExactPassword(t *testing.T) {
	for _, tc := range []struct{ name, origin, phase, action, expected string }{
		{"email", "https://accounts.google.com", "email", "email", "submitted"},
		{"password", "https://accounts.google.com", "password", "password", "submitted"},
		{"foreign", "https://accounts.google.com.evil.test", "password", "password", "unknown"},
		{"http", "http://accounts.google.com", "password", "password", "unknown"},
		{"captcha", "https://accounts.google.com", "challenge", "password", "manual"},
		{"wrong_identity", "https://accounts.google.com", "wrong_identity", "password", "manual"},
		{"later", "https://myaccount.google.com", "later", "later", "submitted"},
		{"later_interstitial", "https://myaccount.google.com", "later_interstitial", "later", "submitted"},
		{"later_interstitial_zh", "https://myaccount.google.com", "later_interstitial_zh", "later", "submitted"},
		{"later_unrecognized_path", "https://myaccount.google.com", "later_other_path", "later", "unknown"},
		{"later_zh", "https://myaccount.google.com", "later_zh", "later", "submitted"},
		{"later_ambiguous", "https://myaccount.google.com", "ambiguous", "later", "manual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := goja.New()
			_ = vm.Set("testOrigin", tc.origin)
			_ = vm.Set("testPhase", tc.phase)
			_, err := vm.RunString(`
var clicked = 0, filled = '';
var window = {};
function HTMLInputElement() {}
Object.defineProperty(HTMLInputElement.prototype, 'value', {set: function(v){ filled=v; },get:function(){return ''}});
function Event() {}
const element = {getAttribute: () => null, getClientRects: () => [1], dispatchEvent: () => {}, click: () => {clicked++}, value: ''};
const password = Object.assign(new HTMLInputElement(), {getAttribute: element.getAttribute, getClientRects: element.getClientRects, dispatchEvent:element.dispatchEvent});
const later = Object.assign({},element,{innerText: testPhase === 'later_zh' || testPhase === 'later_interstitial_zh' ? '以后再说' : 'Do this later'});
const location = {origin:testOrigin,pathname:testPhase.startsWith('later_interstitial') ? '/interstitials/twosvrequired' : testPhase === 'later_other_path' ? '/other' : '/signinoptions/two-step-verification/enroll-welcome'};
const getComputedStyle = () => ({visibility:'visible'});
const document = {body:{innerText:"Don't get locked out · 2-Step Verification"}, querySelectorAll: function(s){
 if(s.startsWith('a,button')) return testPhase === 'ambiguous' ? [later,later] : [later];
 if(s === 'input[type="password"]') return ['password','wrong_identity'].includes(testPhase) ? [password] : [];
 if(s.startsWith('input[type="email"]')) return testPhase === 'email' ? [password] : [];
 if(s.startsWith('input[type="tel"]')) return testPhase === 'challenge' ? [element] : [];
 if(s.startsWith('#profileIdentifier')) return testPhase === 'wrong_identity' ? [Object.assign({},element,{textContent:'other@example.test'})] : [];
 if(s === '#passwordNext' || s === '#identifierNext') return [element];
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
			fn, ok := goja.AssertFunction(value)
			if !ok {
				t.Fatal("not a function")
			}
			result, err := fn(goja.Undefined(), vm.ToValue(tc.action), vm.ToValue("  synthetic password  "), vm.ToValue("user@example.test"))
			if err != nil || result.String() != tc.expected {
				t.Fatal("unexpected login phase", err, result)
			}
			if tc.expected == "submitted" && tc.action == "password" && vm.Get("filled").String() != "  synthetic password  " {
				t.Fatal("password was modified")
			}
			if tc.expected != "submitted" && vm.Get("clicked").ToInteger() != 0 {
				t.Fatal("unsafe page clicked")
			}
		})
	}
}
