package management

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// taskInput is persisted separately from the public task. It is never returned,
// logged, formatted into errors, or copied into audit entries.
type taskInput struct {
	Channels    []c.Channel        `json:"channels,omitempty"`
	RequestID   string             `json:"requestId"`
	Model       string             `json:"model"`
	Verify      bool               `json:"verify"`
	Legacy      bool               `json:"legacy"`
	Force       bool               `json:"force"`
	Action      string             `json:"action"`
	Expected    c.ExpectedVersions `json:"expected"`
	Credentials []c.Credentials    `json:"credentials"`
	Proxies     []*string          `json:"proxies,omitempty"`
}

func (in taskInput) proxy(index int) *string {
	if index >= 0 && index < len(in.Proxies) {
		return in.Proxies[index]
	}
	return nil // Old durable task inputs do not have proxy fields.
}

type taskService struct {
	store        c.Store
	accounts     c.AccountCoordinator
	verifier     c.Verifier
	clock        c.Clock
	newID        c.IDGenerator
	limit        int
	mu           sync.Mutex
	started      bool
	closed       bool
	wake         chan struct{}
	done         chan struct{}
	cancel       context.CancelFunc
	activeID     string
	activeCancel context.CancelFunc
}

var _ c.TaskService = (*taskService)(nil)

func NewTaskService(o c.TaskServiceOptions) (c.TaskService, error) {
	if o.Store == nil || o.Accounts == nil || o.Verifier == nil || o.QueueLimit < 0 {
		return nil, invalid()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.NewID == nil {
		o.NewID = c.NewID
	}
	if o.QueueLimit == 0 {
		o.QueueLimit = 100
	}
	return &taskService{store: o.Store, accounts: o.Accounts, verifier: o.Verifier, clock: o.Clock, newID: o.NewID, limit: o.QueueLimit, wake: make(chan struct{}, 1), done: make(chan struct{})}, nil
}

func (s *taskService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return c.NewError(c.CodeInvalidState)
	}
	if s.started {
		return nil
	}
	_, err := s.store.RecoverTasks(ctx)
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	go s.run(workerCtx)
	s.signal()
	return nil
}

func (s *taskService) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *taskService) Submit(ctx context.Context, a c.Admission) (c.TaskAccepted, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return c.TaskAccepted{}, c.NewError(c.CodeInvalidState)
	}
	if a.IdempotencyKey == "" {
		return c.TaskAccepted{}, c.NewError(c.CodeIdempotencyKeyRequired)
	}
	if len(a.Items) == 0 || len(a.Items) > c.MaxBatchItems || len(a.PrivateInput) == 0 {
		return c.TaskAccepted{}, invalid()
	}
	a.QueueLimit = s.limit
	a.Capture.Mode = captureMode(a.Kind)
	admitted, err := s.store.AdmitTask(ctx, a)
	// An admission error may follow a durable commit. The database scan, not
	// this response or an in-memory queue, determines what can be claimed.
	s.signal()
	if err != nil {
		return c.TaskAccepted{}, err
	}
	// A replay always has the original queued acceptance, not live task state.
	return c.TaskAccepted{TaskID: admitted.Accepted.TaskID, Status: c.TaskQueued}, nil
}

func (s *taskService) Cancel(ctx context.Context, id string, p c.Principal) (c.Task, error) {
	if !p.Has(c.ScopeTasksWrite) {
		return c.Task{}, c.NewError(c.CodeForbidden)
	}
	record, err := s.store.RequestTaskCancellation(ctx, id)
	if err != nil {
		return c.Task{}, err
	}
	s.mu.Lock()
	if s.activeID == id && s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	s.signal()
	return record.Task, nil
}

func (s *taskService) CancelForKey(ctx context.Context, id string) error {
	if err := s.store.CancelQueuedForKey(ctx, id); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *taskService) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// change always starts from the latest revision. In particular an HTTP cancel
// racing an item completion cannot be erased by a worker's stale snapshot.
func (s *taskService) change(id string, fn func(*c.TaskRecord)) (c.TaskRecord, error) {
	return s.changeChecked(id, func(r *c.TaskRecord) error { fn(r); return nil })
}

// changeChecked rejects an inconsistent projection before any revision is saved.
func (s *taskService) changeChecked(id string, fn func(*c.TaskRecord) error) (c.TaskRecord, error) {
	ctx, cancel := cleanupContext()
	defer cancel()
	for i := 0; i < 5; i++ {
		r, err := s.store.GetTask(ctx, id)
		if err != nil {
			return r, err
		}
		if r.Status.Terminal() {
			return r, nil
		}
		rev := r.Revision
		if err := fn(&r); err != nil {
			return r, err
		}
		r.UpdatedAt = c.NewTimestamp(s.clock())
		r.Counts = countItems(r.Items)
		saved, err := s.store.SaveTask(ctx, r, rev)
		if err == nil {
			return saved, nil
		}
		if c.PublicError(err).Code != c.CodeVersionConflict {
			return r, err
		}
	}
	return c.TaskRecord{}, c.NewError(c.CodePersistenceError)
}

func countItems(items []c.TaskItem) c.TaskCounts {
	n := c.TaskCounts{Total: len(items)}
	for _, i := range items {
		switch i.Status {
		case c.TaskQueued:
			n.Queued++
		case c.TaskRunning:
			n.Running++
		case c.TaskSucceeded:
			n.Succeeded++
		case c.TaskFailed:
			n.Failed++
		case c.TaskCancelled:
			n.Cancelled++
		case c.TaskInterrupted:
			n.Interrupted++
		}
	}
	return n
}

func (s *taskService) stopCode(ctx context.Context, id string) c.ErrorCode {
	clean, cancel := cleanupContext()
	defer cancel()
	r, err := s.store.GetTask(clean, id)
	if err == nil && r.CancellationRequested {
		return c.CodeCancelled
	}
	if ctx.Err() != nil {
		return c.CodeInterrupted
	}
	return ""
}

func (s *taskService) executeWork(ctx context.Context, work c.TaskWork) {
	id := work.Record.TaskID
	// A failed status write must not leave a claimed task eligible for replay.
	// Retry a bounded interruption projection while the process is still alive;
	// durable account evidence remains authoritative if storage is unavailable.
	defer func() {
		clean, cancel := cleanupContext()
		r, readErr := s.store.GetTask(clean, id)
		cancel()
		if readErr == nil && !r.Status.Terminal() {
			s.finish(id, c.CodeInterrupted)
		}
	}()
	var input taskInput
	decodeErr := json.Unmarshal(work.PrivateInput, &input)
	if input.Channels == nil {
		input.Channels = []c.Channel{c.ChannelPlayground}
	} // Historical admitted tasks were fixed to Playground.
	if decodeErr != nil || !validTaskInput(work.Record.Kind, len(work.Record.Items), input) {
		s.finish(id, c.CodeInterrupted)
		return
	}
	preconditions, err := taskPreconditions(work)
	if err != nil {
		s.finish(id, c.CodeInterrupted)
		return
	}
	for index := range work.Record.Items {
		if code := s.stopCode(ctx, id); code != "" {
			s.finish(id, code)
			return
		}
		r, err := s.change(id, func(r *c.TaskRecord) {
			r.Items[index].Status = c.TaskRunning
			r.Items[index].Stage = "running"
			r.Items[index].Progress = 1
		})
		if err != nil {
			return
		}
		if r.Status.Terminal() {
			return
		}
		item := r.Items[index]
		progress := func(stage string, value int) {
			if value < 1 {
				value = 1
			}
			if value > 99 {
				value = 99
			}
			_, _ = s.change(id, func(r *c.TaskRecord) {
				if r.Items[index].Status == c.TaskRunning {
					if value > r.Items[index].Progress {
						r.Items[index].Progress = value
					}
					r.Items[index].Stage = safeStage(stage)
				}
			})
		}
		op := c.OperationContext{RequestID: input.RequestID, CreatedByKeyID: r.CreatedByKeyID, Progress: progress}
		if r.Kind == c.TaskImport || r.Kind == c.TaskReplace {
			op.Upload = &c.UploadIdentity{TaskID: id, ItemIndex: index, Kind: r.Kind}
		}
		count := 0
		if work.PreconditionSet != nil {
			count = work.PreconditionSet.TargetCount
		}
		result, runErr := s.executeItem(ctx, r.Kind, item, index, input, op, id, preconditions[index], count)
		// Read original durable evidence even if cancellation or a publication
		// error was returned after commit. Never synthesize from live versions.
		var receipt *c.UploadReceipt
		if op.Upload != nil {
			clean, cancel := cleanupContext()
			receipt, err = s.store.GetUploadReceipt(clean, *op.Upload)
			cancel()
			if err != nil {
				runErr = c.NewError(c.CodePersistenceError)
			}
			if receipt == nil && runErr == nil {
				runErr = c.NewError(c.CodePersistenceError)
			}
		}
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		code := c.ErrorCode("")
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) {
				code = s.stopCode(ctx, id)
				if code == "" {
					code = c.CodeCancelled
				}
			} else if errors.Is(runErr, context.DeadlineExceeded) {
				code = c.CodeVerificationTimeout
			} else {
				code = c.PublicError(runErr).Code
				if code == c.CodeCancelled && ctx.Err() != nil {
					if stopped := s.stopCode(ctx, id); stopped != "" {
						code = stopped
					}
				}
			}
		}
		auditAccountID := item.AccountID
		if receipt != nil {
			auditAccountID = receipt.AccountID
		}
		if auditErr := s.audit(input.RequestID, r.CreatedByKeyID, string(r.Kind), id, auditAccountID, code); auditErr != nil && code == "" {
			code = c.CodeAuditPersistenceFailed
		}
		_, err = s.change(id, func(r *c.TaskRecord) {
			i := &r.Items[index]
			if receipt != nil {
				attachReceipt(i, receipt)
			}
			if result != nil {
				i.Result = result
				if result.Verification != nil {
					value := result.Verification.AuthIndex
					i.Index = &value
				}
			}
			if code != "" && i.Result != nil && i.Result.Verification != nil {
				i.Result.Verification.FailFinalization(code)
			}
			i.Progress = 100
			if code == "" {
				i.Status = c.TaskSucceeded
				i.Stage = "completed"
			} else {
				i.Error = c.NewError(code)
				i.Status = c.TaskFailed
				i.Stage = "failed"
				if code == c.CodeCancelled {
					i.Status = c.TaskCancelled
					i.Stage = "cancelled"
				}
				if code == c.CodeInterrupted {
					i.Status = c.TaskInterrupted
					i.Stage = "interrupted"
				}
			}
		})
		if err != nil {
			return
		}
		if code == c.CodeCancelled || code == c.CodeInterrupted {
			s.finish(id, code)
			return
		}
	}
	s.finish(id, "")
}

func validTaskInput(kind c.TaskKind, items int, input taskInput) bool {
	if _, err := c.NormalizeVerificationChannels(input.Channels); err != nil {
		return false
	}
	if !c.ValidRequestID(input.RequestID) {
		return false
	}
	switch kind {
	case c.TaskImport:
		return c.ValidModel(input.Model) && len(input.Credentials) == items
	case c.TaskReplace:
		return c.ValidModel(input.Model) && len(input.Credentials) == 1 && input.Expected.Validate(!input.Legacy) == nil
	case c.TaskTest:
		return c.ValidModel(input.Model) && (input.Action == string(c.VerificationModel) || input.Action == string(c.VerificationConnection))
	case c.TaskBatch:
		return input.Action == "enable" || input.Action == "disable" || input.Action == "archive"
	case c.TaskArchive, c.TaskRestore, c.TaskReload, c.TaskReloadAuth:
		return true
	}
	return false
}

func attachReceipt(item *c.TaskItem, receipt *c.UploadReceipt) {
	copy := *receipt
	item.Upload = &copy
	item.AccountID = receipt.AccountID
	index := receipt.Index
	item.Index = &index
	if item.Result == nil {
		item.Result = &c.ItemResult{Upload: &c.UploadOnlyResult{Success: true, Stage: "uploaded", Versions: receipt.Versions}}
	}
}

func (s *taskService) finish(id string, code c.ErrorCode) {
	_, _ = s.change(id, func(r *c.TaskRecord) {
		for i := range r.Items {
			item := &r.Items[i]
			if !item.Status.Terminal() {
				item.Progress = 100
				item.Status = c.TaskInterrupted
				item.Stage = "interrupted"
				item.Error = c.NewError(c.CodeInterrupted)
				if code == c.CodeCancelled {
					item.Status = c.TaskCancelled
					item.Stage = "cancelled"
					item.Error = c.NewError(code)
				}
			}
		}
		n := countItems(r.Items)
		r.Status = c.TaskFailed
		outcomes := 0
		for _, total := range []int{n.Succeeded, n.Failed, n.Cancelled, n.Interrupted} {
			if total > 0 {
				outcomes++
			}
		}
		if outcomes > 1 {
			r.Status = c.TaskPartial
		} else if n.Succeeded == n.Total {
			r.Status = c.TaskSucceeded
		} else if n.Interrupted > 0 {
			r.Status = c.TaskInterrupted
		} else if n.Cancelled > 0 {
			r.Status = c.TaskCancelled
		}
		if code != "" {
			r.Error = c.NewError(code)
		}
		now := c.NewTimestamp(s.clock())
		r.FinishedAt = &now
		ids := []string{}
		for _, item := range r.Items {
			if item.AccountID != "" {
				ids = append(ids, item.AccountID)
			}
		}
		if len(ids) > 0 {
			r.Result = &c.TaskResult{AccountIDs: ids}
		}
	})
}

func (s *taskService) audit(req, key, action, task, account string, code c.ErrorCode) error {
	ctx, cancel := cleanupContext()
	defer cancel()
	id, err := s.newID("audit")
	if err != nil {
		return err
	}
	e := c.AuditEntry{AuditID: id, RequestID: req, CreatedByKeyID: key, Action: action, TaskID: task, AccountID: account, Timestamp: c.NewTimestamp(s.clock()), Outcome: "succeeded"}
	if code != "" {
		e.Outcome = "failed"
		if code == c.CodeCancelled {
			e.Outcome = "cancelled"
		}
		if code == c.CodeInterrupted {
			e.Outcome = "interrupted"
		}
		e.Error = c.NewError(code)
	}
	return s.store.AppendAudit(ctx, e)
}

func safeStage(stage string) string {
	switch stage {
	case "queued", "running", "preparing", "draining", "committing", "uploaded", "verifying", "opening", "connecting", "generating", "completed", "failed", "cancelled", "interrupted", "connection_verified", "model_verified":
		return stage
	}
	return "running"
}
