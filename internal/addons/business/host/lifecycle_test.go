package host

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseWaitsForWholeOperationAndHonorsDeadline(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "config.json"), nil)
	_, done, ok := h.beginOperation(httptest.NewRequest("POST", "/api/addons/business/collect", nil))
	if !ok {
		t.Fatal("initial operation rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close returned before operation persistence phase ended", err)
	}
	if _, _, ok := h.beginOperation(httptest.NewRequest("GET", "/api/addons/business/status", nil)); ok {
		t.Fatal("new operation accepted after closing")
	}
	done()
	if err := h.Close(context.Background()); err != nil {
		t.Fatal("completed operation prevented close", err)
	}
}
