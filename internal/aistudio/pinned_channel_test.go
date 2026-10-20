package aistudio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type pinnedRPCFunc func(context.Context, RPCRequest) (*RPCResponse, error)

func (f pinnedRPCFunc) Do(ctx context.Context, r RPCRequest) (*RPCResponse, error) { return f(ctx, r) }

func TestPinnedClientRejectsMissingWrongChannelAndWrongAccountBeforeTransport(t *testing.T) {
	calls := 0
	transport := pinnedRPCFunc(func(context.Context, RPCRequest) (*RPCResponse, error) {
		calls++
		return nil, errors.New("unexpected catalogue")
	})
	protected := ProtectedTransportFunc(func(context.Context, GenerateRequest, RPCRequest) (*RPCResponse, error) {
		calls++
		return nil, errors.New("unexpected generation")
	})
	client, err := NewClient(ClientOptions{Transport: transport, Protected: protected, PinnedChannel: ChannelBuild})
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.Background(), ContextWithAccountLease(context.Background(), &AccountLease{account: &Account{ID: "synthetic@example.invalid"}, channel: ChannelPlayground}), ContextWithAccountLease(context.Background(), &AccountLease{account: &Account{ID: "other@example.invalid"}, channel: ChannelBuild})} {
		if _, err := client.Generate(ctx, GenerateRequest{AccountID: "synthetic@example.invalid", Model: "synthetic-model"}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("wrong lease accepted: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("wrong lease caused %d upstream calls", calls)
	}
}

func TestOrdinaryClientBuildRetainsCachedPlaygroundDefaults(t *testing.T) {
	const accountID = "synthetic@example.invalid"
	model := Model{ID: "synthetic-model", OutputTokenLimit: 1024, Methods: []string{"generateContent"}, Capabilities: map[string]bool{}}
	account := &Account{ID: accountID, buildModels: []Model{model}}
	pool := NewAccountPool([]*Account{account}, 1)
	calls := 0
	client, err := NewClient(ClientOptions{
		Transport: pinnedRPCFunc(func(context.Context, RPCRequest) (*RPCResponse, error) {
			return nil, errors.New("ordinary cached catalogue was bypassed")
		}),
		Protected: ProtectedTransportFunc(func(_ context.Context, _ GenerateRequest, rpc RPCRequest) (*RPCResponse, error) {
			calls++
			if rpc.Method != "ProxyStreamedCall" {
				return nil, errors.New("ordinary Build route changed")
			}
			var envelope []json.RawMessage
			var body string
			var request struct {
				GenerationConfig struct {
					MaxOutputTokens int `json:"maxOutputTokens"`
				} `json:"generationConfig"`
			}
			if json.Unmarshal(rpc.Body, &envelope) != nil || json.Unmarshal(envelope[1], &body) != nil || json.Unmarshal([]byte(body), &request) != nil || request.GenerationConfig.MaxOutputTokens != 37 {
				return nil, errors.New("ordinary model defaults changed")
			}
			encoded, _ := json.Marshal([]any{[]any{[]any{`{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}`}}})
			return &RPCResponse{StatusCode: 200, Header: http.Header{"Content-Type": []string{JSONProtobufContentType}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.catalogs[accountID] = modelCatalog{entries: map[string]modelEntry{model.ID: {model: model, defaults: GenerationDefaults{MaxOutputTokens: 37}}}}
	ctx := ContextWithAccountLease(context.Background(), &AccountLease{account: account, pool: pool, channel: ChannelBuild})
	events, err := client.Generate(ctx, GenerateRequest{AccountID: accountID, Model: model.ID, Contents: []Content{{Role: RoleUser, Parts: []Part{{Text: "OK"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Kind == EventError {
			t.Fatal(event.Err)
		}
	}
	if calls != 1 {
		t.Fatalf("ordinary calls=%d", calls)
	}
}
