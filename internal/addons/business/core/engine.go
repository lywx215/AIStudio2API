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
	"unicode"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

const authURL = "https://business.gemini.google/auth/getoxsrf"
const baseURL = "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/"

func New(parent context.Context, accounts []vault.Account, limit int, timeout time.Duration, transport http.RoundTripper) *Engine {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil // Business never inherits another module's process proxy.
		t.ResponseHeaderTimeout = 20 * time.Second
		t.TLSHandshakeTimeout = 10 * time.Second
		t.MaxIdleConnsPerHost = 16
		transport = t
	}
	ctx, cancel := context.WithCancel(parent)
	e := &Engine{entries: make(map[string]*entry), limit: max(1, min(limit, 16)), timeout: timeout, ctx: ctx, cancel: cancel, policy: defaultBackoffPolicy(),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	e.Reconcile(accounts, limit, timeout)
	context.AfterFunc(ctx, e.client.CloseIdleConnections)
	return e
}

type upstreamError struct{ status int }

func (*upstreamError) Error() string { return "Business upstream request failed" }

// Generate leases an account for a new session or an exact, completed native
// prefix. It never replays historical generations or changes an account mid-turn.
func (e *Engine) Generate(ctx context.Context, selection contract.Selection, input contract.Request, emit func(contract.Event) error) (result error) {
	caller := ctx
	if err := ctx.Err(); err != nil {
		return classifyOperation(err, StagePreflight, false, false, caller)
	}
	if err := validateAttachments(input.Attachments); err != nil {
		return classifyOperation(err, StagePreflight, false, false, caller)
	}
	if err := validateConversation(input); err != nil {
		return classifyOperation(err, StagePreflight, false, false, caller)
	}
	for _, value := range input.Generation {
		if !json.Valid(value) {
			return classifyOperation(contract.Fail(400, "INVALID_ARGUMENT", "Invalid Business generation parameter JSON"), StagePreflight, false, false, caller)
		}
	}
	a, previous, err := e.acquireConversation(ctx, selection, input)
	if err != nil {
		return classifyOperation(err, StagePreflight, false, false, caller)
	}
	stage := StageAuth
	defer func() {
		if result != nil {
			result = classifyOperation(result, stage, a.submitted, a.outputStarted, caller)
		}
		e.release(a, result)
	}()
	ctx = a.ctx
	a.client = e.client
	if proxy := a.account.Credential.ProxyURL; proxy != "" {
		if vault.ValidateProxy(proxy) != nil {
			return contract.Fail(400, "INVALID_ARGUMENT", "Invalid Business proxy")
		}
		u, _ := url.Parse(proxy)
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy, t.ResponseHeaderTimeout, t.TLSHandshakeTimeout = http.ProxyURL(u), 20*time.Second, 10*time.Second
		a.client = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer a.client.CloseIdleConnections()
	}
	if err = e.ensureToken(ctx, a); err != nil {
		return err
	}
	if input.GoogleSearch {
		stage = StageConfigLookup
		if err := e.requireGoogleSearch(ctx, a); err != nil {
			return err
		}
	}
	sessionName := ""
	if previous != nil {
		sessionName = previous.session
	} else {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return contract.Fail(500, "INTERNAL", "Business session randomness is unavailable")
		}
		name := hex.EncodeToString(id[:])
		var session struct {
			Session struct {
				Name string `json:"name"`
			} `json:"session"`
		}
		body := map[string]any{"configId": a.account.Credential.ConfigID, "additionalParams": map[string]string{"token": "-"}, "createSessionRequest": map[string]any{"session": map[string]string{"name": name, "displayName": name}}}
		stage = StageSession
		response, err := e.post(ctx, "widgetCreateSession", a, body)
		if err == nil {
			err = decodeBounded(response.Body, &session, 1<<20)
			response.Body.Close()
		}
		if err != nil {
			return err
		}
		if !validSessionName(session.Session.Name) {
			return contract.Fail(502, "DATA_LOSS", "Business returned an invalid session")
		}
		sessionName = session.Session.Name
		e.mu.Lock()
		e.sessionsCreated++
		e.mu.Unlock()
	}
	stage = StageUpload
	fileIDs, err := e.uploadAttachments(ctx, a, sessionName, input.Attachments)
	if err != nil {
		return err
	}
	if previous != nil {
		fileIDs, err = appendContextFileIDs(previous.fileIDs, fileIDs)
		if err != nil {
			return err
		}
	}
	generation := map[string]any{}
	for k, v := range input.Generation {
		generation[k] = v
	}
	if !selection.Auto {
		generation["modelId"] = selection.ModelID
	}
	assist := map[string]any{"session": sessionName, "query": map[string]any{"parts": input.QueryParts}, "answerGenerationMode": "NORMAL", "assistGenerationConfig": generation, "assistSkippingMode": "REQUEST_ASSIST"}
	if input.GoogleSearch {
		assist["toolsSpec"] = map[string]any{"webGroundingSpec": map[string]any{}}
	}
	if len(fileIDs) > 0 {
		assist["fileIds"] = fileIDs
	}
	body := map[string]any{"configId": a.account.Credential.ConfigID, "additionalParams": map[string]string{"token": "-"}, "streamAssistRequest": assist}
	stage = StageGenerate
	if previous != nil {
		e.mu.Lock()
		e.continuationAttempts++
		e.mu.Unlock()
	}
	response, err := e.post(ctx, "widgetStreamAssist", a, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	stage = StageStream
	var output []contract.Part
	err = decodeStream(response.Body, func(event contract.Event) error {
		if len(input.History) > 0 {
			if len(output)+len(event.Parts) > 4096 {
				return contract.Fail(502, "RESOURCE_EXHAUSTED", "Business output exceeds 4096 parts")
			}
			output = append(output, event.Parts...)
		}
		if len(event.Parts) > 0 {
			a.outputStarted = true
		}
		if err := emit(event); err != nil {
			return &deliveryError{err}
		}
		return nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		e.verifyModel(a, selection)
		e.completeConversation(caller, a, selection, input, sessionName, fileIDs, output)
	}
	return err
}

func publicError(err error) error {
	if err == nil {
		return nil
	}
	return classifyOperation(err, StagePreflight, false, false, nil)
}
func decodeBounded(r io.Reader, value any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return &transportError{err}
	}
	if int64(len(data)) > limit {
		return &protocolError{errors.New("invalid upstream body")}
	}
	if failure, ok := rpcEnvelopeFailure(data); ok {
		return failure
	}
	if err := json.Unmarshal(data, value); err != nil {
		return &protocolError{err}
	}
	return nil
}

func validSessionName(value string) bool {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\\:?#%") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || parts[len(parts)-2] != "sessions" {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if c != '-' && c != '_' && c != '.' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
				return false
			}
		}
	}
	return true
}
func (e *Engine) post(ctx context.Context, action string, a *lease, body any) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, contract.Fail(400, "INVALID_ARGUMENT", "Business request parameters cannot be serialized")
	}
	return e.postEncoded(ctx, action, a, bytes.NewReader(data), int64(len(data)))
}

// postEncoded is shared by bounded small JSON and streaming base64 uploads.
// It does not retry or switch accounts, and only the fixed origin is accepted.
func (e *Engine) postEncoded(ctx context.Context, action string, a *lease, body io.Reader, contentLength int64) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if action != "widgetCreateSession" && action != "widgetAddContextFile" && action != "widgetStreamAssist" && action != "lookupWidgetConfig" {
		return nil, contract.Fail(400, "INVALID_ARGUMENT", "Unsupported Business transport action")
	}
	r, err := http.NewRequestWithContext(ctx, "POST", baseURL+action, body)
	if err != nil {
		return nil, err
	}
	r.ContentLength = contentLength
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://business.gemini.google")
	r.Header.Set("Referer", "https://business.gemini.google/")
	r.Header.Set("User-Agent", userAgent(a.account.Credential))
	if action == "widgetStreamAssist" {
		a.submitted = true
	}
	client := e.leaseClient(a)
	if action == "widgetStreamAssist" {
		var closeIdle func()
		client, closeIdle = generationClient(client)
		defer closeIdle()
	}
	response, err := client.Do(r)
	if err != nil {
		return nil, &transportError{err}
	}
	if response.StatusCode != 200 {
		return nil, httpFailure(response, e.policy.now())
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
	if a.token != "" && e.policy.now().Before(a.expires) {
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
	response, err := e.leaseClient(a).Do(r)
	if err != nil {
		return &transportError{err}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return httpFailure(response, e.policy.now())
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return &transportError{err}
	}
	if len(data) > 64<<10 {
		return &protocolError{errors.New("invalid auth response")}
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte(")]}'")))
	if failure, ok := rpcEnvelopeFailure(data); ok {
		return failure
	}
	var auth struct {
		KeyID     string `json:"keyId"`
		XSRFToken string `json:"xsrfToken"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.KeyID == "" || auth.XSRFToken == "" {
		return &protocolError{errors.New("invalid auth response")}
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(auth.XSRFToken, "="))
	if err != nil || len(key) == 0 {
		return &protocolError{errors.New("invalid auth key")}
	}
	defer clear(key)
	now := e.policy.now()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": auth.KeyID})
	payload, _ := json.Marshal(map[string]any{"iss": "https://business.gemini.google", "aud": "https://biz-discoveryengine.googleapis.com", "sub": "csesidx/" + a.account.Credential.CSESIndex, "iat": now.Unix(), "exp": now.Unix() + 300, "nbf": now.Unix()})
	message := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	token := message + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return e.commitToken(a, token, now.Add(240*time.Second), now)
}
