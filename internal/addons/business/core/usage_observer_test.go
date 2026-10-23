package core

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func TestGenerationObserverTracksActualLeaseAndNetworkAttempts(t *testing.T) {
	attempts, targeted := 0, 0
	ctx := contract.WithGenerationObserver(context.Background(), contract.GenerationObserver{
		Target: func(model, id string) {
			targeted++
			if model != "business-test" || id != account().ID {
				t.Fatal("wrong leased identity", model, id)
			}
		},
		Upstream: func() { attempts++ },
	})
	network := 0
	e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		network++
		if attempts != network || targeted != 1 {
			t.Fatal("observer lost across account lease context", attempts, network, targeted)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "getoxsrf"):
			return reply(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
		case strings.HasSuffix(r.URL.Path, "widgetCreateSession"):
			return reply(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		default:
			return reply(200, stream), nil
		}
	}))
	defer e.Close()
	if err := e.Generate(ctx, contract.Selection{PublicID: "business-test", Auto: true}, request(), func(contract.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if targeted != 1 || attempts != 3 {
		t.Fatal(targeted, attempts)
	}
}
