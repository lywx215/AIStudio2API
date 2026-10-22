package browser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func TestBusinessLoginManagerCancelKeepsBusyUntilCleanup(t *testing.T) {
	var manager Manager
	started, cleanupStarted, allowCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := &fakeDriver{command: func(ctx context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, close: func() error { close(cleanupStarted); <-allowCleanup; return nil }}
	finished := make(chan error, 1)
	go func() {
		_, err := manager.Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
		finished <- err
	}()
	<-started
	if _, err := manager.Collect(context.Background(), Options{}, nil); !errors.Is(err, ErrBusy) {
		t.Fatal("parallel collection accepted")
	}
	manager.Cancel()
	<-cleanupStarted
	if _, err := manager.Collect(context.Background(), Options{}, nil); !errors.Is(err, ErrBusy) {
		t.Fatal("new collection accepted while old cleanup pending")
	}
	close(allowCleanup)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ready := &fakeDriver{command: readyCommand(t, nil)}
	if credential, err := manager.Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return ready, nil }); err != nil || credential == (vault.Credential{}) {
		t.Fatal("manager did not permit a fresh collection after cleanup")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Collect(context.Background(), Options{}, nil); !errors.Is(err, ErrClosed) {
		t.Fatal("closed manager accepted collection")
	}
}

func TestBusinessLoginManagerCloseBoundAndCleanupFailure(t *testing.T) {
	var manager Manager
	started, cleanupStarted, allowCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := &fakeDriver{command: func(ctx context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, close: func() error { close(cleanupStarted); <-allowCleanup; return ErrCleanup }}
	finished := make(chan error, 1)
	go func() {
		_, err := manager.Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil })
		finished <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("manager Close did not honor deadline")
	}
	<-cleanupStarted
	closeResult := make(chan error, 1)
	go func() { closeResult <- manager.Close(context.Background()) }()
	// Ensure Close has selected the still-active run before releasing cleanup.
	manager.mu.Lock()
	run := manager.active
	manager.mu.Unlock()
	if run == nil {
		t.Fatal("collection unexpectedly released before cleanup")
	}
	close(allowCleanup)
	if err := <-finished; !errors.Is(err, ErrCleanup) {
		t.Fatal("cleanup failure lost from collection")
	}
	if err := <-closeResult; !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
}

func TestBusinessLoginCleanupFailureBlocksFurtherCollection(t *testing.T) {
	var manager Manager
	d := &fakeDriver{command: readyCommand(t, nil), close: func() error { return ErrCleanup }}
	if _, err := manager.Collect(context.Background(), Options{}, func(context.Context, Options) (Driver, error) { return d, nil }); !errors.Is(err, ErrCleanup) {
		t.Fatal(err)
	}
	if _, err := manager.Collect(context.Background(), Options{}, nil); !errors.Is(err, ErrCleanup) {
		t.Fatal("unconfirmed cleanup allowed another browser")
	}
	if err := manager.Close(context.Background()); !errors.Is(err, ErrCleanup) {
		t.Fatal("Close lost an earlier cleanup failure")
	}
}
