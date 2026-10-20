// Package managementmaintenance owns the single process-level retention loop.
// It does not recover, schedule or execute management tasks.
package managementmaintenance

import (
	"context"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type Pruner interface {
	Prune(context.Context, c.Timestamp) error
}

type Options struct {
	Clock c.Clock
	// A catalog code is the only error detail allowed to reach a log callback.
	OnFailure func(c.ErrorCode)
}

type Service struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Start prunes immediately and then hourly, sequentially, with a <=30s context
// for each run. The caller must Close and join before closing its Pruner/store.
func Start(parent context.Context, pruner Pruner, options Options) (*Service, error) {
	return start(parent, pruner, options, nil, c.MaintenanceTimeout)
}

// ticks/budget are private test seams; production timing is not configurable.
func start(parent context.Context, pruner Pruner, options Options, ticks <-chan time.Time, budget time.Duration) (*Service, error) {
	if parent == nil || pruner == nil {
		return nil, c.NewError(c.CodeInvalidState)
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if budget <= 0 || budget > c.MaintenanceTimeout {
		budget = c.MaintenanceTimeout
	}
	ctx, cancel := context.WithCancel(parent)
	service := &Service{cancel: cancel, done: make(chan struct{})}
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(c.MaintenanceInterval)
		ticks = ticker.C
	}
	go func() {
		defer close(service.done)
		if ticker != nil {
			defer ticker.Stop()
		}
		run := func() {
			if ctx.Err() != nil {
				return
			}
			pruneCtx, pruneCancel := context.WithTimeout(ctx, budget)
			cutoff := c.NewTimestamp(options.Clock().Add(-c.Retention))
			err := pruner.Prune(pruneCtx, cutoff)
			pruneCancel()
			if err != nil && ctx.Err() == nil && options.OnFailure != nil {
				options.OnFailure(c.PublicError(err).Code)
			}
		}
		run()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
				run()
			}
		}
	}()
	return service, nil
}

// Close is idempotent and waits for an in-flight prune to return. A context-
// ignoring store must not be closed underneath that work; G1 is responsible for
// honoring cancellation between short bounded transactions.
func (service *Service) Close() {
	if service == nil {
		return
	}
	service.cancel()
	<-service.done
}
