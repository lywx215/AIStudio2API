package aistudio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

type upstreamRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upstreamRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpstreamObserverAtSendBoundary(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "google-429", true: "connection-error"}[failure], func(t *testing.T) {
			parent, attempt, sends := 0, 0, 0
			ctx := ContextWithUpstreamRequestObserver(t.Context(), func() { parent++ })
			ctx = ContextWithUpstreamRequestObserver(context.WithoutCancel(ctx), func() { attempt++ })
			client := &http.Client{Transport: upstreamRoundTripFunc(func(*http.Request) (*http.Response, error) {
				sends++
				if parent != sends || attempt != sends {
					t.Fatal("send preceded observer notification")
				}
				if failure {
					return nil, errors.New("synthetic connection failure")
				}
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("quota"))}, nil
			})}
			if parent != 0 || attempt != 0 {
				t.Fatal("context preparation was mistaken for a send")
			}
			for range 2 {
				r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.example.invalid/rpc", nil)
				response, err := doUpstreamRequest(client, r)
				if failure != (err != nil) {
					t.Fatalf("response=%v err=%v", response, err)
				}
				if response != nil {
					_ = response.Body.Close()
				}
			}
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.example.invalid/rpc", nil)
			if _, err := doUpstreamRequest(client, r); !errors.Is(err, context.Canceled) || sends != 2 || parent != 2 || attempt != 2 {
				t.Fatal("canceled local request was marked as sent", err)
			}
		})
	}
}

type upstreamWorkerRuntime struct {
	workerRuntime
	send func(context.Context) (*camoufoxnative.ProtectedResponse, error)
}

func (runtime upstreamWorkerRuntime) SendProtected(ctx context.Context, _ string, _ http.Header, _ []byte) (*camoufoxnative.ProtectedResponse, error) {
	return runtime.send(ctx)
}

func TestNativeWorkerObservesProtectedSendAndSkipsCanceled(t *testing.T) {
	observed, sends := 0, 0
	ctx := ContextWithUpstreamRequestObserver(t.Context(), func() { observed++ })
	worker := &NativeWorker{runtime: upstreamWorkerRuntime{send: func(context.Context) (*camoufoxnative.ProtectedResponse, error) {
		sends++
		if observed != sends {
			t.Fatal("protected send was not observed")
		}
		return nil, errors.New("synthetic protected connection failure")
	}}}
	if _, err := worker.SendProtected(ctx, ProtectedRequest{}); err == nil || observed != 1 || sends != 1 {
		t.Fatal("failed protected send was not counted", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := worker.SendProtected(ctx, ProtectedRequest{}); !errors.Is(err, context.Canceled) || observed != 1 || sends != 1 {
		t.Fatal("canceled protected send was counted", err)
	}
}
