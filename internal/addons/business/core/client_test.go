package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerationHeaderWaitUsesLeaseDeadlineWithoutChangingAuthClient(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-time.After(80 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 15 * time.Millisecond
	base := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer base.CloseIdleConnections()
	client, cleanup := generationClient(base)
	defer cleanup()
	if transport.ResponseHeaderTimeout != 15*time.Millisecond || client.CheckRedirect == nil {
		t.Fatal("auth deadline or redirect guard changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("generation cut off before lease deadline", err)
	}
	response.Body.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, "GET", server.URL, nil)
	if _, err = client.Do(req2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("generation ignored request deadline", err)
	}
	if requests.Load() != 2 {
		t.Fatal("generation replayed")
	}
}
