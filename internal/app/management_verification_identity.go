package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

const managementVerificationIdentityURL = "https://accounts.google.com/ListAccounts?gpsia=1&source=ChromiumBrowser&json=standard"

func managementVerificationIdentity(ctx context.Context, state aistudio.StorageState, env managementVerificationEnvironment) (string, int, error) {
	client, err := aistudio.NewProxyHTTPClient(env.Proxy)
	if err != nil {
		return "", 0, c.NewError(c.CodeVerificationFailed)
	}
	defer client.CloseIdleConnections()
	return managementVerificationIdentityWithClient(ctx, client, state)
}

func managementVerificationIdentityWithClient(ctx context.Context, client *http.Client, state aistudio.StorageState) (string, int, error) {
	// Copy the client so neither redirects nor a cookie jar can change which
	// isolated account supplied these cookies. Never forward them to a redirect.
	isolated := *client
	isolated.Jar = nil
	isolated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, managementVerificationIdentityURL, strings.NewReader(" "))
	if err != nil {
		return "", 0, c.NewError(c.CodeVerificationFailed)
	}
	cookie, err := state.CookieHeader(managementVerificationIdentityURL, time.Now())
	if err != nil || cookie == "" {
		return "", 0, c.NewError(c.CodeVerificationFailed)
	}
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", "https://www.google.com")
	response, err := isolated.Do(request)
	if err != nil {
		return "", 0, c.NewError(c.CodeVerificationFailed)
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status < 200 || status >= 300 {
		return "", status, c.NewError(c.CodeVerificationFailed)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", status, c.NewError(c.CodeVerificationFailed)
	}
	email, err := managementVerificationPrimaryIdentity(raw)
	return email, status, err
}

// Field positions are defined by Chromium's ParseListAccountsData, not an
// email regex over HTML or a caller-supplied account label. See the source link
// recorded in docs/management/deliveries/G4.md. Unknown/legacy shapes fail closed.
func managementVerificationPrimaryIdentity(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.HasPrefix(raw, []byte(")]}'")) {
		raw = bytes.TrimSpace(raw[4:])
	}
	var root []json.RawMessage
	if json.Unmarshal(raw, &root) != nil || len(root) < 2 {
		return "", c.NewError(c.CodeVerificationFailed)
	}
	var marker string
	var accounts []json.RawMessage
	if json.Unmarshal(root[0], &marker) != nil || marker != "gaia.l.a.r" || json.Unmarshal(root[1], &accounts) != nil || len(accounts) == 0 {
		return "", c.NewError(c.CodeVerificationFailed)
	}
	// AI Studio and both WAA backends use authuser=0. The first row is the
	// primary account; finding the requested email in a later row is insufficient.
	var primary []json.RawMessage
	if json.Unmarshal(accounts[0], &primary) != nil || len(primary) < 16 {
		return "", c.NewError(c.CodeVerificationFailed)
	}
	var email, gaiaID string
	var valid, signedOut, verified int
	if bytes.Equal(bytes.TrimSpace(primary[9]), []byte("null")) || bytes.Equal(bytes.TrimSpace(primary[14]), []byte("null")) || bytes.Equal(bytes.TrimSpace(primary[15]), []byte("null")) ||
		json.Unmarshal(primary[3], &email) != nil || json.Unmarshal(primary[10], &gaiaID) != nil ||
		json.Unmarshal(primary[9], &valid) != nil || json.Unmarshal(primary[14], &signedOut) != nil || json.Unmarshal(primary[15], &verified) != nil ||
		valid != 1 || signedOut != 0 || verified != 1 || !managementVerificationDecimalID(gaiaID) {
		return "", c.NewError(c.CodeVerificationFailed)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !managementVerificationNativeID(email) {
		return "", c.NewError(c.CodeVerificationFailed)
	}
	return email, nil
}

func managementVerificationDecimalID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
