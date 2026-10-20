package app

import (
	"context"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type g0MaintenanceStore struct {
	c.Store
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	closed    chan struct{}
}

func (s *g0MaintenanceStore) Prune(ctx context.Context, _ c.Timestamp) error {
	close(s.entered)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	return ctx.Err()
}
func (s *g0MaintenanceStore) Close() error { close(s.closed); return nil }

func g0WaitMaintenance(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance lifecycle test timed out")
	}
}

func TestManagementMaintenanceJoinsBeforeStoreClose(t *testing.T) {
	store := &g0MaintenanceStore{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	manager := &runtimeManager{management: &c.ProcessServices{Store: store}}
	if err := manager.startManagementMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	g0WaitMaintenance(t, store.entered)
	first := manager.managementMaintenance
	if err := manager.startManagementMaintenance(context.Background()); err != nil || manager.managementMaintenance != first {
		t.Fatal("duplicate process timer")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	g0WaitMaintenance(t, store.cancelled)
	select {
	case <-store.closed:
		t.Fatal("store closed under an active prune")
	default:
	}
	close(store.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process close did not join maintenance")
	}
	g0WaitMaintenance(t, store.closed)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.startManagementMaintenance(context.Background()); c.PublicError(err).Code != c.CodeInvalidState {
		t.Fatal("maintenance restarted after process close")
	}
}
