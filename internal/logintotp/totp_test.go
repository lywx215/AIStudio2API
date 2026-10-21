package logintotp

import (
	"testing"
	"time"
)

func TestRFC6238SHA1SixDigits(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		at   int64
		code string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		got, err := Code(secret, time.Unix(tc.at, 0))
		if err != nil || got != tc.code {
			t.Fatal("TOTP vector failed at", tc.at)
		}
	}
	if got, _ := Code("gezd-gnbv gy3tqojqgezdgnbvgy3tqojq", time.Unix(59, 0)); got != "287082" {
		t.Fatal("Manager normalization differs")
	}
	for _, invalid := range []string{"", "123456", "secret!", "otpauth://totp/test"} {
		if Valid(invalid) {
			t.Fatal("invalid secret accepted")
		}
	}
	if Remaining(time.Unix(22, 1)) >= 8*time.Second || Remaining(time.Unix(30, 0)) != 30*time.Second {
		t.Fatal("period boundary incorrect")
	}
}
