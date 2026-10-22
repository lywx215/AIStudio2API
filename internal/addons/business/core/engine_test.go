package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func account() vault.Account {
	return vault.Account{ID: "acct-" + strings.Repeat("a", 32), Enabled: true, Epoch: 1, Credential: vault.Credential{SecureCSES: "synthetic-cookie", HostCOSES: "synthetic-host", CSESIndex: "synthetic-index", ConfigID: "synthetic-config"}}
}
func request() contract.Request {
	return contract.Request{QueryParts: []map[string]string{{"text": "hello"}}, Generation: map[string]json.RawMessage{"temperature": json.RawMessage("0")}}
}

const stream = `[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"text":"你"}}}]}}},{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"好"}}}]}}}]`

func TestHTTPFlowJWTCacheAndExactPayload(t *testing.T) {
	counts := map[string]int{}
	key := []byte("synthetic-hmac-key")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		counts[r.URL.Path]++
		if r.URL.Path == "/auth/getoxsrf" {
			if r.URL.Host != "business.gemini.google" || r.Header.Get("Authorization") != "" || !strings.Contains(r.Header.Get("Cookie"), "__Secure-C_SES=synthetic-cookie") {
				t.Fatal("auth isolation failed")
			}
			return reply(200, `)]}'`+"\n"+`{"keyId":"synthetic-key","xsrfToken":"`+base64.RawURLEncoding.EncodeToString(key)+`"}`), nil
		}
		if r.URL.Host != "biz-discoveryengine.googleapis.com" || r.Header.Get("Cookie") != "" {
			t.Fatal("cookie escaped auth origin")
		}
		jwt := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(jwt) != 3 {
			t.Fatal("invalid JWT")
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(jwt[0] + "." + jwt[1]))
		if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != jwt[2] {
			t.Fatal("invalid signature")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid request")
		}
		if string(body["configId"]) != `"synthetic-config"` {
			t.Fatal("wrong config")
		}
		if strings.HasSuffix(r.URL.Path, "widgetCreateSession") {
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		}
		var assist map[string]json.RawMessage
		_ = json.Unmarshal(body["streamAssistRequest"], &assist)
		if string(assist["session"]) != `"sessions/synthetic"` {
			t.Fatal("wrong session")
		}
		var generation map[string]json.RawMessage
		_ = json.Unmarshal(assist["assistGenerationConfig"], &generation)
		if string(generation["temperature"]) != "0" || string(generation["modelId"]) != `"observed-model"` {
			t.Fatal("parameter or exact model lost")
		}
		return reply(200, stream), nil
	})
	e := New(context.Background(), []vault.Account{account()}, 2, time.Second, transport)
	defer e.Close()
	for range 2 {
		text := ""
		finished := 0
		err := e.Generate(context.Background(), contract.Selection{ModelID: "observed-model"}, request(), func(event contract.Event) error {
			for _, p := range event.Parts {
				text += p.Text
			}
			if event.Finished {
				finished++
			}
			return nil
		})
		if err != nil || text != "你好" || finished != 1 {
			t.Fatalf("text=%s finished=%d err=%v", text, finished, err)
		}
	}
	if counts["/auth/getoxsrf"] != 1 || counts["/v1alpha/locations/global/widgetCreateSession"] != 2 || counts["/v1alpha/locations/global/widgetStreamAssist"] != 2 {
		t.Fatal(counts)
	}
}

func TestAuthFailureInvalidatesWithoutReplayingGeneration(t *testing.T) {
	calls := 0
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return reply(401, `{"secret":"must not leak"}`), nil
	}))
	defer e.Close()
	err := e.Generate(context.Background(), contract.Selection{ModelID: "test"}, request(), func(contract.Event) error { return nil })
	if err == nil || strings.Contains(err.Error(), "secret") || e.States()[0].State != "auth_required" {
		t.Fatal(err)
	}
	_ = e.Generate(context.Background(), contract.Selection{ModelID: "test"}, request(), func(contract.Event) error { return nil })
	if calls != 1 {
		t.Fatal("replayed unauthenticated request")
	}
}

func TestLeaseAndShutdownCancellation(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	e := New(context.Background(), []vault.Account{account()}, 1, time.Minute, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	done := make(chan error, 1)
	go func() {
		done <- e.Generate(context.Background(), contract.Selection{ModelID: "test"}, request(), func(contract.Event) error { return nil })
	}()
	<-entered
	err := e.Generate(context.Background(), contract.Selection{ModelID: "test"}, request(), func(contract.Event) error { return nil })
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 429 {
		t.Fatalf("expected capacity rejection: %v", err)
	}
	e.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel became success")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel HTTP")
	}
}

func TestStreamStrictCompletionAndBoundaries(t *testing.T) {
	for _, body := range []string{`[`, `[]`, `[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS"}}}]`, strings.TrimSuffix(stream, "]"), stream + `{}`, `[{"streamAssistResponse":{"answer":{"state":"FAILED"}}}]`, `[{"error":{"message":"synthetic secret"}}]`} {
		finished := false
		err := decodeStream(strings.NewReader(body), func(e contract.Event) error { finished = finished || e.Finished; return nil })
		if err == nil || finished || strings.Contains(err.Error(), "synthetic secret") {
			t.Fatalf("accepted bad stream: %q err=%v", body, err)
		}
	}
	reader := &oneByteReader{data: []byte(stream)}
	text := ""
	if err := decodeStream(reader, func(e contract.Event) error {
		for _, p := range e.Parts {
			text += p.Text
		}
		return nil
	}); err != nil || text != "你好" {
		t.Fatal(text, err)
	}
}

func TestNoCredentialRedirectAndNoWrongAccountScope(t *testing.T) {
	calls := 0
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		response := reply(302, "")
		response.Header.Set("Location", "https://other.example/private")
		return response, nil
	}))
	defer e.Close()
	if err := e.Generate(context.Background(), contract.Selection{ModelID: "test", AccountIDs: []string{"acct-wrong"}}, request(), func(contract.Event) error { return nil }); err == nil || calls != 0 {
		t.Fatal("wrong account scope used")
	}
	if err := e.Generate(context.Background(), contract.Selection{ModelID: "test"}, request(), func(contract.Event) error { return nil }); err == nil || calls != 1 {
		t.Fatal("credential request followed a redirect")
	}
}

func TestThoughtSignatureAndUnsupportedPart(t *testing.T) {
	body := `[{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"reason","thought":true,"thoughtSignature":"opaque-fixture"}}}]}}}]`
	var part contract.Part
	if err := decodeStream(strings.NewReader(body), func(event contract.Event) error {
		if len(event.Parts) > 0 {
			part = event.Parts[0]
		}
		return nil
	}); err != nil || !part.Thought || part.ThoughtSignature != "opaque-fixture" {
		t.Fatal(part, err)
	}
	bad := strings.Replace(body, `"text":"reason"`, `"functionCall":{"name":"tool"}`, 1)
	if err := decodeStream(strings.NewReader(bad), func(contract.Event) error { return nil }); err == nil {
		t.Fatal("silently discarded an unsupported part")
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
