package host

import (
	"context"
	"errors"
	"net/http"
)

// Account persistence is part of the HTTP operation, not just the browser's
// lifetime. No new operation can be registered after Close starts waiting.
func (h *Host) beginOperation(r *http.Request) (*http.Request, func(), bool) {
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	if h.closed.Load() {
		return nil, nil, false
	}
	h.operations.Add(1)
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(h.ctx, cancel)
	return r.WithContext(ctx), func() {
		stop()
		cancel()
		h.operations.Done()
	}, true
}

func (h *Host) closeOperations(ctx context.Context) error {
	h.operationMu.Lock()
	h.closed.Store(true)
	h.cancel()
	h.operationMu.Unlock()
	err := h.collector.Close(ctx)
	done := make(chan struct{})
	go func() { h.operations.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
