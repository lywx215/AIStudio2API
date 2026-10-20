package managementmaintenance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type pruneFunc func(context.Context, c.Timestamp) error

func (f pruneFunc) Prune(ctx context.Context, cutoff c.Timestamp) error { return f(ctx, cutoff) }

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance synchronization timed out")
		var zero T
		return zero
	}
}

func TestInitialAndPeriodicRetentionWithRedactedError(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 123456789, time.FixedZone("fixture", 8*3600))
	calls := make(chan c.Timestamp, 3)
	failures := make(chan c.ErrorCode, 3)
	ticks := make(chan time.Time)
	var count atomic.Int32
	service, err := start(context.Background(), pruneFunc(func(ctx context.Context, cutoff c.Timestamp) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > c.MaintenanceTimeout {
			t.Error("missing/bad per-run deadline")
		}
		current := count.Add(1)
		calls <- cutoff
		if current == 1 {
			return errors.New("fixture-secret-must-not-be-logged")
		}
		return nil
	}), Options{Clock: func() time.Time { return now }, OnFailure: func(code c.ErrorCode) { failures <- code }}, ticks, c.MaintenanceTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	first := receive(t, calls)
	if first != c.NewTimestamp(now.Add(-c.Retention)) {
		t.Fatal("retention cutoff is not detached UTC milliseconds")
	}
	if code := receive(t, failures); code != c.CodeInternalError {
		t.Fatalf("error projection=%s", code)
	}
	select {
	case ticks <- now:
	case <-time.After(3 * time.Second):
		t.Fatal("periodic tick was not read")
	}
	second := receive(t, calls)
	if second != first || count.Load() != 2 {
		t.Fatal("periodic prune did not follow initial run")
	}
	service.Close()
	select {
	case ticks <- now:
		t.Fatal("closed maintenance still reads ticks")
	default:
	}
}

func TestCloseCancelsAndJoinsInFlightPrune(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	service, err := Start(context.Background(), pruneFunc(func(ctx context.Context, _ c.Timestamp) error {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		close(returned)
		return ctx.Err()
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	closed := make(chan struct{})
	go func() { service.Close(); close(closed) }()
	receive(t, cancelled)
	select {
	case <-closed:
		t.Fatal("Close returned while prune was still active")
	default:
	}
	close(release)
	receive(t, closed)
	receive(t, returned)
	service.Close()
}

func TestTimeoutDoesNotPreventTheNextRun(t *testing.T) {
	ticks := make(chan time.Time)
	timedOut := make(chan error, 1)
	completed := make(chan struct{}, 1)
	var count atomic.Int32
	service, err := start(context.Background(), pruneFunc(func(ctx context.Context, _ c.Timestamp) error {
		if count.Add(1) == 1 {
			<-ctx.Done()
			timedOut <- ctx.Err()
			return ctx.Err()
		}
		completed <- struct{}{}
		return nil
	}), Options{}, ticks, 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := receive(t, timedOut); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prune deadline=%v", err)
	}
	select {
	case ticks <- time.Now():
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance stopped after deadline")
	}
	receive(t, completed)
}

func TestRejectMissingDependenciesAndRespectParentCancellation(t *testing.T) {
	if _, err := Start(nil, pruneFunc(func(context.Context, c.Timestamp) error { return nil }), Options{}); err == nil {
		t.Fatal("nil parent accepted")
	}
	if _, err := Start(context.Background(), nil, Options{}); err == nil {
		t.Fatal("nil pruner accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	service, err := Start(ctx, pruneFunc(func(context.Context, c.Timestamp) error { calls.Add(1); return nil }), Options{})
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	if calls.Load() != 0 {
		t.Fatal("prune started after parent was cancelled")
	}
}
