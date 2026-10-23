package core

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

// Every entry field belongs to Engine.mu. A retired entry remains in the ID
// table until its active lease releases; pending is the latest desired version
// (nil means deleted). It is never possible to lease old and new versions of an
// account simultaneously, including a rapid addon off/on cycle.
type entry struct {
	account                           vault.Account
	token                             string
	expires, cooldown, authVerifiedAt time.Time
	state                             string
	verifiedModels                    map[string]time.Time
	active                            *lease
	retired                           bool
	pending                           *entry
	lastError                         errorRecord
	consecutiveFailures               int
}

// account/timeout/ctx are captured at acquisition and never changed. token and
// expires are touched only by the owning generation goroutine. released is
// protected by Engine.mu, so a duplicate/late release cannot affect a new lease.
type lease struct {
	owner                    *entry
	account                  vault.Account
	token                    string
	expires                  time.Time
	timeout                  time.Duration
	ctx                      context.Context
	cancel                   context.CancelFunc
	stopEngine               func() bool
	released                 bool
	submitted, outputStarted bool
	client                   *http.Client
}

type Engine struct {
	mu                                    sync.Mutex
	entries                               map[string]*entry
	order                                 []string
	limit, active, cursor                 int
	suspended                             bool
	client                                *http.Client
	ctx                                   context.Context
	cancel                                context.CancelFunc
	timeout                               time.Duration
	policy                                backoffPolicy
	conversations                         map[string]*conversation
	sessionsCreated, continuationAttempts uint64
	conversationEpoch                     uint64
}

type AccountState struct {
	ID                     string               `json:"id"`
	State                  string               `json:"state"`
	AuthVerified           bool                 `json:"auth_verified"`
	AuthVerifiedAt         time.Time            `json:"auth_verified_at,omitempty,omitzero"`
	VerifiedModels         map[string]time.Time `json:"verified_models,omitempty"`
	LastErrorKind          ErrorKind            `json:"last_error_kind,omitempty"`
	LastErrorStage         ErrorStage           `json:"last_error_stage,omitempty"`
	LastErrorStatus        string               `json:"last_error_status,omitempty"`
	LastErrorReason        string               `json:"last_error_reason,omitempty"`
	LastErrorHTTPStatus    int                  `json:"last_error_http_status,omitempty"`
	LastErrorSubmitted     bool                 `json:"last_error_submitted"`
	LastErrorOutputStarted bool                 `json:"last_error_output_started"`
	LastErrorAt            time.Time            `json:"last_error_at,omitempty,omitzero"`
	ConsecutiveFailures    int                  `json:"consecutive_failures"`
	CooldownUntil          time.Time            `json:"cooldown_until,omitempty,omitzero"`
}

// Persist only safe primitive classifications, never an underlying error,
// response message, credentials or a transport URL inside account state.
type errorRecord struct {
	kind              ErrorKind
	stage             ErrorStage
	status, reason    string
	httpStatus        int
	submitted, output bool
	at                time.Time
}

type RuntimeSnapshot struct {
	Accounts      []AccountState       `json:"accounts"`
	Limit         int                  `json:"limit"`
	Active        int                  `json:"active"`
	Closed        bool                 `json:"closed"`
	Suspended     bool                 `json:"suspended"`
	Conversations ConversationSnapshot `json:"conversations"`
}

func freshEntry(account vault.Account) *entry {
	return &entry{account: account, state: "ready", verifiedModels: map[string]time.Time{}}
}

func sameCredentialVersion(left, right vault.Account) bool {
	return left.Credential == right.Credential && left.Epoch == right.Epoch
}

func desiredEntry(previous *entry, account vault.Account) *entry {
	next := freshEntry(account)
	if previous != nil && sameCredentialVersion(previous.account, account) {
		next.token, next.expires, next.cooldown = previous.token, previous.expires, previous.cooldown
		next.authVerifiedAt, next.state = previous.authVerifiedAt, previous.state
		next.lastError, next.consecutiveFailures = previous.lastError, previous.consecutiveFailures
		for model, at := range previous.verifiedModels {
			next.verifiedModels[model] = at
		}
	}
	return next
}

// Configure changes only the scheduler settings captured by future leases.
// Reducing the limit never cancels or changes the deadlines of existing work.
func (e *Engine) Configure(limit int, timeout time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return
	}
	e.limit, e.timeout = max(1, min(limit, 16)), timeout
}

// Suspend turns off admission without discarding unchanged account state. An
// active version drains into an identical pending snapshot, so late callbacks
// from cancelled work cannot overwrite JWT/cooldown/model evidence on resume.
func (e *Engine) Suspend() {
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return
	}
	e.suspended = true
	e.conversationEpoch++
	clear(e.conversations)
	cancels := []context.CancelFunc{}
	for _, current := range e.entries {
		if current.active == nil {
			continue
		}
		if !current.retired {
			current.retired, current.pending = true, desiredEntry(current, current.account)
		}
		cancels = append(cancels, current.active.cancel)
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Reconcile installs the latest account metadata without recreating Engine.
// Label-only changes preserve JWTs, cooldowns and verification. Credential/Epoch
// changes reset evidence; disabling/deleting cancels only that account's lease.
func (e *Engine) Reconcile(accounts []vault.Account, limit int, timeout time.Duration) {
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return
	}
	e.limit, e.timeout = max(1, min(limit, 16)), timeout
	e.suspended = false
	wanted := make(map[string]bool, len(accounts))
	order := make([]string, 0, len(accounts))
	cancels := []context.CancelFunc{}
	for _, account := range accounts {
		// Vault validation guarantees unique IDs; defensively avoid an ambiguous
		// duplicate if an internal caller supplies an unvalidated snapshot.
		if wanted[account.ID] {
			continue
		}
		wanted[account.ID] = true
		order = append(order, account.ID)
		previous := e.entries[account.ID]
		if previous == nil {
			e.entries[account.ID] = freshEntry(account)
			continue
		}
		if previous.retired {
			source := previous.pending
			if source == nil {
				source = previous
			}
			previous.pending = desiredEntry(source, account)
			continue
		}
		if sameCredentialVersion(previous.account, account) && previous.account.Enabled == account.Enabled {
			// Leases own value snapshots, so metadata can change under this lock
			// without changing an in-flight request's credential or account epoch.
			previous.account = account
			continue
		}
		next := desiredEntry(previous, account)
		if previous.active == nil {
			e.entries[account.ID] = next
		} else {
			previous.retired, previous.pending = true, next
			cancels = append(cancels, previous.active.cancel)
		}
	}
	for id, previous := range e.entries {
		if wanted[id] {
			continue
		}
		if previous.active == nil {
			delete(e.entries, id)
		} else {
			previous.retired, previous.pending = true, nil
			cancels = append(cancels, previous.active.cancel)
		}
	}
	e.order = order
	e.pruneConversationsLocked()
	if len(order) == 0 {
		e.cursor = 0
	} else {
		e.cursor %= len(order)
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (e *Engine) Snapshot() RuntimeSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneConversationsLocked()
	now := e.policy.now()
	result := RuntimeSnapshot{Accounts: []AccountState{}, Limit: e.limit, Active: e.active, Closed: e.ctx.Err() != nil, Suspended: e.suspended}
	result.Conversations = e.conversationSnapshotLocked()
	for _, id := range e.order {
		current := e.entries[id]
		if current == nil {
			continue
		}
		visible := current
		if current.retired {
			visible = current.pending
			if visible == nil {
				continue
			}
		}
		state := visible.state
		switch {
		case !visible.account.Enabled:
			state = "disabled"
		case current.retired:
			state = "draining"
		case e.suspended:
			state = "suspended"
		case current.active != nil:
			state = "busy"
		case state == "cooldown" && !now.Before(visible.cooldown):
			state = "ready"
		}
		item := AccountState{ID: id, State: state, AuthVerified: !result.Closed && visible.token != "" && now.Before(visible.expires), AuthVerifiedAt: visible.authVerifiedAt, VerifiedModels: map[string]time.Time{}}
		item.LastErrorKind, item.LastErrorStage = visible.lastError.kind, visible.lastError.stage
		item.LastErrorStatus, item.LastErrorReason = visible.lastError.status, visible.lastError.reason
		item.LastErrorHTTPStatus = visible.lastError.httpStatus
		item.LastErrorSubmitted, item.LastErrorOutputStarted = visible.lastError.submitted, visible.lastError.output
		item.LastErrorAt, item.ConsecutiveFailures, item.CooldownUntil = visible.lastError.at, visible.consecutiveFailures, visible.cooldown
		for model, at := range visible.verifiedModels {
			item.VerifiedModels[model] = at
		}
		result.Accounts = append(result.Accounts, item)
	}
	return result
}

func (e *Engine) States() []AccountState { return e.Snapshot().Accounts }

func (e *Engine) Close() {
	e.cancel()
	e.mu.Lock()
	e.conversationEpoch++
	clear(e.conversations)
	cancels := []context.CancelFunc{}
	for _, current := range e.entries {
		if current.active != nil {
			cancels = append(cancels, current.active.cancel)
		}
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	e.client.CloseIdleConnections()
}

func (e *Engine) acquire(ctx context.Context, scope []string) (*lease, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.acquireLocked(ctx, scope)
}

func (e *Engine) acquireLocked(ctx context.Context, scope []string) (*lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.ctx.Err() != nil {
		return nil, contract.Fail(503, "UNAVAILABLE", "Business runtime stopped")
	}
	if e.suspended {
		return nil, contract.Fail(503, "UNAVAILABLE", "Business runtime is suspended")
	}
	if e.active >= e.limit {
		return nil, contract.Fail(429, "RESOURCE_EXHAUSTED", "Business concurrency limit reached")
	}
	for offset := 0; offset < len(e.order); offset++ {
		index := (e.cursor + offset) % len(e.order)
		current := e.entries[e.order[index]]
		if current == nil || current.retired || !current.account.Enabled || current.active != nil || current.state == "auth_required" || e.policy.now().Before(current.cooldown) || (len(scope) > 0 && !slices.Contains(scope, current.account.ID)) {
			continue
		}
		leaseCtx, cancel := context.WithTimeout(ctx, e.timeout)
		result := &lease{owner: current, account: current.account, token: current.token, expires: current.expires, timeout: e.timeout, ctx: leaseCtx, cancel: cancel}
		result.stopEngine = context.AfterFunc(e.ctx, cancel)
		current.active = result
		e.active++
		e.cursor = (index + 1) % len(e.order)
		return result, nil
	}
	return nil, contract.Fail(503, "UNAVAILABLE", "No eligible Business account is available")
}

func (e *Engine) currentLeaseLocked(item *lease) bool {
	return !item.released && !e.suspended && e.entries[item.account.ID] == item.owner && item.owner.active == item && !item.owner.retired && e.ctx.Err() == nil && item.ctx.Err() == nil
}

func (e *Engine) commitToken(item *lease, token string, expires, verifiedAt time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.currentLeaseLocked(item) {
		return context.Canceled
	}
	item.token, item.expires = token, expires
	item.owner.token, item.owner.expires, item.owner.authVerifiedAt = token, expires, verifiedAt
	return nil
}

func (e *Engine) verifyModel(item *lease, selection contract.Selection) {
	model := selection.ModelID
	if selection.Auto {
		model = "auto"
	}
	if model == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.currentLeaseLocked(item) {
		item.owner.verifiedModels[model] = e.policy.now().UTC()
	}
}

func (e *Engine) release(item *lease, err error) {
	e.mu.Lock()
	if item.released || item.owner.active != item {
		e.mu.Unlock()
		return
	}
	item.released = true
	item.owner.active = nil
	e.active--
	current := item.owner
	if e.entries[item.account.ID] == current {
		if current.retired {
			if current.pending == nil {
				delete(e.entries, item.account.ID)
			} else {
				e.entries[item.account.ID] = current.pending
			}
			current.pending = nil
		} else {
			e.applyOutcomeLocked(current, err, item)
		}
	}
	e.mu.Unlock()
	item.stopEngine()
	item.cancel()
}

func (e *Engine) applyOutcomeLocked(current *entry, err error, item *lease) {
	if err == nil {
		current.state, current.cooldown = "ready", time.Time{}
		current.lastError, current.consecutiveFailures = errorRecord{}, 0
		return
	}
	outcome, ok := err.(*OperationError)
	if !ok {
		outcome = classifyOperation(err, StagePreflight, item.submitted, item.outputStarted, nil)
	}
	now := e.policy.now()
	current.lastError = errorRecord{kind: outcome.Kind, stage: outcome.Stage, status: outcome.Status, reason: outcome.Reason, httpStatus: outcome.HTTPStatus, submitted: outcome.Submitted, output: outcome.OutputStarted, at: now.UTC()}
	switch outcome.Kind {
	case ErrorAuthentication:
		current.token = ""
		current.expires = time.Time{}
		current.state = "auth_required"
	case ErrorQuotaExhausted, ErrorTransient, ErrorProtocol:
		if current.consecutiveFailures < 1<<30 {
			current.consecutiveFailures++
		}
		delay := e.policy.delay(outcome.Kind, current.consecutiveFailures, time.Duration(outcome.RetryAfterSeconds)*time.Second)
		current.state, current.cooldown = "cooldown", now.Add(delay)
	default:
		// Parameters, local budgets, downstream writes and caller cancellation
		// never penalize an otherwise usable account or reset its failure count.
		current.state = "ready"
	}
}
