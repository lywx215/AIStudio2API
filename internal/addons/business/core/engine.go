// Package core implements the Business HTTP transport without using AI Studio
// WAA, its account pool, session store or request DTOs.
package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

const authURL = "https://business.gemini.google/auth/getoxsrf"
const baseURL = "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/"

func New(parent context.Context, accounts []vault.Account, limit int, timeout time.Duration, transport http.RoundTripper) *Engine {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 20 * time.Second
		t.TLSHandshakeTimeout = 10 * time.Second
		t.MaxIdleConnsPerHost = 16
		transport = t
	}
	ctx, cancel := context.WithCancel(parent)
	e := &Engine{entries: make(map[string]*entry), limit: max(1, min(limit, 16)), timeout: timeout, ctx: ctx, cancel: cancel,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	e.Reconcile(accounts, limit, timeout)
	context.AfterFunc(ctx, e.client.CloseIdleConnections)
	return e
}

type upstreamError struct{ status int }

func (*upstreamError) Error() string { return "Business upstream request failed" }

// Generate leases one account for a fresh session. There is no replay of a
// submitted generation, no account switch after output and no shared history.
func (e *Engine) Generate(ctx context.Context, selection contract.Selection, input contract.Request, emit func(contract.Event) error) (result error) {
	if err := ctx.Err(); err != nil {
		return publicError(err)
	}
	if err := validateAttachments(input.Attachments); err != nil {
		return err
	}
	a, err := e.acquire(ctx, selection.AccountIDs)
	if err != nil {
		return publicError(err)
	}
	var operationErr error
	defer func() { e.release(a, operationErr) }()
	ctx = a.ctx
	if err = e.ensureToken(ctx, a); err != nil {
		operationErr = err
		return publicError(err)
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	name := hex.EncodeToString(id[:])
	var session struct {
		Session struct {
			Name string `json:"name"`
		} `json:"session"`
	}
	body := map[string]any{"configId": a.account.Credential.ConfigID, "additionalParams": map[string]string{"token": "-"}, "createSessionRequest": map[string]any{"session": map[string]string{"name": name, "displayName": name}}}
	response, err := e.post(ctx, "widgetCreateSession", a, body)
	if err == nil {
		err = decodeBounded(response.Body, &session, 1<<20)
		response.Body.Close()
	}
	if err != nil {
		operationErr = err
		return publicError(err)
	}
	if session.Session.Name == "" {
		return contract.Fail(502, "DATA_LOSS", "Business returned an invalid session")
	}
	fileIDs, err := e.uploadAttachments(ctx, a, session.Session.Name, input.Attachments)
	if err != nil {
		operationErr = err
		return publicError(err)
	}
	generation := map[string]any{}
	for k, v := range input.Generation {
		generation[k] = v
	}
	if !selection.Auto {
		generation["modelId"] = selection.ModelID
	}
	assist := map[string]any{"session": session.Session.Name, "query": map[string]any{"parts": input.QueryParts}, "answerGenerationMode": "NORMAL", "assistGenerationConfig": generation, "assistSkippingMode": "REQUEST_ASSIST"}
	if len(fileIDs) > 0 {
		assist["fileIds"] = fileIDs
	}
	body = map[string]any{"configId": a.account.Credential.ConfigID, "additionalParams": map[string]string{"token": "-"}, "streamAssistRequest": assist}
	response, err = e.post(ctx, "widgetStreamAssist", a, body)
	if err != nil {
		operationErr = err
		return publicError(err)
	}
	defer response.Body.Close()
	err = decodeStream(response.Body, emit)
	operationErr = err
	if ctx.Err() != nil {
		return publicError(ctx.Err())
	}
	if err == nil {
		e.verifyModel(a, selection)
	}
	return publicError(err)
}

func publicError(err error) error {
	if err == nil {
		return nil
	}
	var failure *contract.Failure
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return contract.Fail(504, "DEADLINE_EXCEEDED", "Business request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return contract.Fail(499, "CANCELLED", "Business request cancelled")
	}
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		if upstream.status == 401 || upstream.status == 403 {
			return contract.Fail(503, "UNAVAILABLE", "Business credentials need refresh")
		}
		if upstream.status == 429 {
			return contract.Fail(429, "RESOURCE_EXHAUSTED", "Business upstream rate limit reached")
		}
	}
	return contract.Fail(502, "UNAVAILABLE", "Business upstream request failed")
}
func decodeBounded(r io.Reader, value any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errors.New("invalid upstream body")
	}
	return json.Unmarshal(data, value)
}
func (e *Engine) post(ctx context.Context, action string, a *lease, body any) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", baseURL+action, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://business.gemini.google")
	r.Header.Set("Referer", "https://business.gemini.google/")
	r.Header.Set("User-Agent", userAgent(a.account.Credential))
	response, err := e.client.Do(r)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, &upstreamError{response.StatusCode}
	}
	return response, nil
}
func userAgent(c vault.Credential) string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "Mozilla/5.0"
}
func (e *Engine) ensureToken(ctx context.Context, a *lease) error {
	// Cached state is copied into this immutable account lease. Refresh commits
	// back only while this exact version remains current; Reconcile never reads
	// or mutates a lease's token or credential snapshot.
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.token != "" && time.Now().Before(a.expires) {
		return nil
	}
	r, err := http.NewRequestWithContext(ctx, "GET", authURL+"?csesidx="+url.QueryEscape(a.account.Credential.CSESIndex), nil)
	if err != nil {
		return err
	}
	r.AddCookie(&http.Cookie{Name: "__Secure-C_SES", Value: a.account.Credential.SecureCSES})
	if a.account.Credential.HostCOSES != "" {
		r.AddCookie(&http.Cookie{Name: "__Host-C_OSES", Value: a.account.Credential.HostCOSES})
	}
	r.Header.Set("User-Agent", userAgent(a.account.Credential))
	response, err := e.client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return &upstreamError{response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return errors.New("invalid auth response")
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte(")]}'")))
	var auth struct {
		KeyID     string `json:"keyId"`
		XSRFToken string `json:"xsrfToken"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.KeyID == "" || auth.XSRFToken == "" {
		return &upstreamError{401}
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(auth.XSRFToken, "="))
	if err != nil || len(key) == 0 {
		return &upstreamError{401}
	}
	defer clear(key)
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": auth.KeyID})
	payload, _ := json.Marshal(map[string]any{"iss": "https://business.gemini.google", "aud": "https://biz-discoveryengine.googleapis.com", "sub": "csesidx/" + a.account.Credential.CSESIndex, "iat": now.Unix(), "exp": now.Unix() + 300, "nbf": now.Unix()})
	message := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	token := message + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return e.commitToken(a, token, now.Add(240*time.Second), now)
}
