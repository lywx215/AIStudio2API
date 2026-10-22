package browser

import (
	"context"
	"errors"
	"sync"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

var (
	ErrBusy   = errors.New("a Business browser collection is already active")
	ErrClosed = errors.New("Business browser collection manager is closed")
)

type collection struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// Manager serializes Business collection independently of AI Studio. Its zero
// value is ready for use. The host supplies its authenticated session context
// to Collect, so logout/expiry cancels both interaction and credential delivery.
// Persistence and credential epoch/CAS remain the host/vault's responsibility.
// A cleanup failure blocks further collection in this manager, since an old
// browser process may still be alive. Close continues to report that failure.
type Manager struct {
	mu         sync.Mutex
	active     *collection
	closed     bool
	cleanupErr error
}

func (m *Manager) Collect(ctx context.Context, options Options, launch Launcher) (credential vault.Credential, err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return vault.Credential{}, ErrClosed
	}
	if m.active != nil {
		m.mu.Unlock()
		return vault.Credential{}, ErrBusy
	}
	if m.cleanupErr != nil {
		m.mu.Unlock()
		return vault.Credential{}, ErrCleanup
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &collection{cancel: cancel, done: make(chan struct{})}
	m.active = run
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		run.err = err
		if errors.Is(err, ErrCleanup) {
			m.cleanupErr = ErrCleanup
		}
		m.active = nil
		close(run.done)
		m.mu.Unlock()
	}()
	return Collect(runCtx, options, launch)
}

// Cancel requests cancellation of the current collection without waiting. A
// new collection remains busy until the old browser's cleanup has finished.
func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		m.active.cancel()
	}
}

// Close prevents future collections, cancels the active one, and waits up to
// ctx's deadline. Cleanup failure is reported even after cancellation.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	run := m.active
	cleanupErr := m.cleanupErr
	if run != nil {
		run.cancel()
	}
	m.mu.Unlock()
	if run == nil {
		return cleanupErr
	}
	select {
	case <-run.done:
		if errors.Is(run.err, ErrCleanup) {
			return ErrCleanup
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
