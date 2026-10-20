package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

func verificationIdentityJSON(t *testing.T, email string, change func([]any)) []byte {
	t.Helper()
	row := make([]any, 16)
	row[0] = "gaia.l.a"
	row[3] = email
	row[9] = 1
	row[10] = "100000000000000000001"
	row[14] = 0
	row[15] = 1
	if change != nil {
		change(row)
	}
	raw, err := json.Marshal([]any{"gaia.l.a.r", []any{row}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManagementVerificationStructuredPrimaryIdentity(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"valid", verificationIdentityJSON(t, "Synthetic@Example.test", nil), "synthetic@example.test"},
		{"anti-xssi", append([]byte(")]}'\n"), verificationIdentityJSON(t, "synthetic@example.test", nil)...), "synthetic@example.test"},
		{"invalid-session", verificationIdentityJSON(t, "synthetic@example.test", func(row []any) { row[9] = 0 }), ""},
		{"signed-out", verificationIdentityJSON(t, "synthetic@example.test", func(row []any) { row[14] = 1 }), ""},
		{"unverified", verificationIdentityJSON(t, "synthetic@example.test", func(row []any) { row[15] = 0 }), ""},
		{"missing-flag", verificationIdentityJSON(t, "synthetic@example.test", func(row []any) { row[14] = nil }), ""},
		{"missing-gaia", verificationIdentityJSON(t, "synthetic@example.test", func(row []any) { row[10] = "" }), ""},
		{"free-form-email", []byte(`<html>synthetic@example.test</html>`), ""},
		{"empty", []byte(`["gaia.l.a.r",[]]`), ""},
		{"truncated", []byte(`["gaia.l.a.r",[[null,null,null,"synthetic@example.test"]]]`), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			email, err := managementVerificationPrimaryIdentity(tc.raw)
			if tc.want == "" {
				if err == nil {
					t.Fatal("unproven identity accepted")
				}
			} else if err != nil || email != tc.want {
				t.Fatalf("email=%s err=%v", email, err)
			}
		})
	}
	var primary, secondary []json.RawMessage
	_ = json.Unmarshal(verificationIdentityJSON(t, "other@example.test", nil), &primary)
	_ = json.Unmarshal(verificationIdentityJSON(t, "synthetic@example.test", nil), &secondary)
	var one, two []json.RawMessage
	_ = json.Unmarshal(primary[1], &one)
	_ = json.Unmarshal(secondary[1], &two)
	rows, _ := json.Marshal(append(one, two...))
	primary[1] = rows
	raw, _ := json.Marshal(primary)
	email, err := managementVerificationPrimaryIdentity(raw)
	if err != nil || email != "other@example.test" {
		t.Fatal("secondary email substituted for authuser=0")
	}
}

type verificationRoundTrip func(*http.Request) (*http.Response, error)

func (f verificationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagementVerificationIdentityRequestAndRedirect(t *testing.T) {
	material := verificationStorage(t, "synthetic-cookie")
	var state aistudio.StorageState
	if json.Unmarshal(material.StorageState, &state) != nil {
		t.Fatal("fixture")
	}
	for _, status := range []int{200, 302, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: verificationRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != managementVerificationIdentityURL || r.Method != http.MethodPost || !strings.Contains(r.Header.Get("Cookie"), "synthetic-cookie") || r.Header.Get("Authorization") != "" {
					t.Fatal("wrong identity request")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://redirect.example.test/"}}, Body: io.NopCloser(strings.NewReader(string(verificationIdentityJSON(t, "synthetic@example.test", nil)))), Request: r}, nil
			})}
			email, _, err := managementVerificationIdentityWithClient(context.Background(), client, state)
			if status == 200 {
				if err != nil || email != "synthetic@example.test" {
					t.Fatal("identity failed")
				}
			} else if err == nil {
				t.Fatal("identity HTTP error accepted")
			}
			if calls != 1 {
				t.Fatal("identity followed redirect/repeated request")
			}
		})
	}
}
