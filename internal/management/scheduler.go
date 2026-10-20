package management

import (
	"context"
	"log"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

const claimBackoffMinimum = 200 * time.Millisecond
const claimBackoffMaximum = 5 * time.Second
const queueScanInterval = time.Second

type claimRetry struct {
	ref   c.QueuedTaskRef
	due   time.Time
	delay time.Duration
	// knownQueued permits another Claim only after a successful queued readback.
	knownQueued bool
	// A successful claim or started execution may only be read/finalized again.
	neverExecute bool
}

func nextClaimBackoff(previous time.Duration) time.Duration {
	if previous < claimBackoffMinimum {
		return claimBackoffMinimum
	}
	if previous >= claimBackoffMaximum/2 {
		return claimBackoffMaximum
	}
	return previous * 2
}

func retryClaim(previous claimRetry, queued bool) claimRetry {
	previous.knownQueued = queued
	previous.delay = nextClaimBackoff(previous.delay)
	previous.due = time.Now().Add(previous.delay)
	return previous
}

// run has one executor. Queries rediscover durable queued work; hints only
// retain uncertain attempts and retry timing. They never establish execution
// authority, and the keyset cursor lets a failing old row yield to later rows.
func (s *taskService) run(ctx context.Context) {
	defer close(s.done)
	defer func() { s.mu.Lock(); s.closed = true; s.mu.Unlock() }()
	retries := map[string]claimRetry{}
	var cursor *c.QueuedTaskRef
	var scanBackoff time.Duration
	for ctx.Err() == nil {
		for id, hint := range retries {
			if ctx.Err() != nil {
				return
			}
			if time.Now().Before(hint.due) {
				continue
			}
			var next *claimRetry
			if hint.knownQueued && !hint.neverExecute {
				next = s.claimAndExecute(ctx, hint)
			} else {
				next = s.reconcileClaim(hint)
			}
			if next == nil {
				delete(retries, id)
			} else {
				retries[id] = *next
			}
		}
		if ctx.Err() != nil {
			return
		}
		page, err := s.store.ListQueuedTasks(ctx, c.QueuedTaskQuery{After: cursor, Limit: c.MaxPageLimit})
		if err != nil {
			log.Print("management task queue read failed; reconciliation will retry")
			scanBackoff = nextClaimBackoff(scanBackoff)
		} else {
			scanBackoff = 0
			for _, ref := range page.Items {
				if ctx.Err() != nil {
					return
				}
				if _, pending := retries[ref.TaskID]; pending {
					continue
				}
				// Retain at most one bounded page of unresolved attempts. Further
				// work stays queued in SQLite until an uncertainty is resolved.
				if len(retries) >= c.MaxPageLimit {
					break
				}
				if next := s.claimAndExecute(ctx, claimRetry{ref: ref}); next != nil {
					retries[ref.TaskID] = *next
				}
			}
			if page.Next != nil {
				if cursor != nil && page.Next.TaskID == cursor.TaskID && page.Next.CreatedAt.Equal(cursor.CreatedAt.Time) {
					log.Print("management task queue cursor is invalid; reconciliation will retry")
					cursor = nil
					scanBackoff = claimBackoffMinimum
				} else {
					copy := *page.Next
					cursor = &copy
					continue
				}
			} else {
				cursor = nil
			}
		}
		delay := queueScanInterval
		if scanBackoff > 0 {
			delay = scanBackoff
		}
		for _, hint := range retries {
			until := time.Until(hint.due)
			if until < delay {
				delay = until
			}
		}
		if delay < 0 {
			delay = 0
		}
		if !s.waitForQueue(ctx, delay) {
			return
		}
	}
}

func (s *taskService) waitForQueue(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (s *taskService) claimAndExecute(ctx context.Context, hint claimRetry) *claimRetry {
	workCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.activeID = hint.ref.TaskID
	s.activeCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.activeID = ""; s.activeCancel = nil; s.mu.Unlock() }()
	work, err := s.store.ClaimTask(workCtx, hint.ref.TaskID)
	if err != nil {
		return s.reconcileClaim(hint)
	}
	hint.neverExecute = true
	if work.Record.TaskID != hint.ref.TaskID || work.Record.Status != c.TaskRunning {
		s.finish(hint.ref.TaskID, c.CodeInterrupted)
	} else if workCtx.Err() != nil {
		code := s.stopCode(workCtx, hint.ref.TaskID)
		if code == "" {
			code = c.CodeInterrupted
		}
		s.finish(hint.ref.TaskID, code)
	} else {
		s.executeWork(workCtx, work)
	}
	return s.reconcileClaim(hint)
}

// A failed Claim may have committed running. A read error leaves an unresolved
// hint; queued is retried with backoff, while running is interrupted without
// executing even one coordinator or verifier call. Completed business is never
// retried after any uncertain status write.
func (s *taskService) reconcileClaim(hint claimRetry) *claimRetry {
	ctx, cancel := cleanupContext()
	defer cancel()
	record, err := s.store.GetTask(ctx, hint.ref.TaskID)
	if err != nil {
		if c.PublicError(err).Code == c.CodeNotFound {
			return nil
		}
		next := retryClaim(hint, false)
		return &next
	}
	if record.Status.Terminal() {
		return nil
	}
	if record.Status == c.TaskQueued && !hint.neverExecute {
		next := retryClaim(hint, true)
		return &next
	}
	// This method runs only after this executor's Claim/execute returns; it is
	// never a concurrent poll of a known active worker.
	hint.neverExecute = true
	s.finish(hint.ref.TaskID, c.CodeInterrupted)
	current, err := s.store.GetTask(ctx, hint.ref.TaskID)
	if err == nil && current.Status.Terminal() {
		return nil
	}
	if err != nil && c.PublicError(err).Code == c.CodeNotFound {
		return nil
	}
	next := retryClaim(hint, false)
	return &next
}
