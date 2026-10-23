package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

const conversationTTL = 15 * time.Minute
const maxConversations = 128
const maxConversationsPerPrincipal = 16

// Aggregate process-local diagnostics contain no session names or user data.
type ConversationSnapshot struct {
	Retained             int    `json:"retained"`
	Limit                int    `json:"limit"`
	TTLSeconds           int    `json:"ttl_seconds"`
	SessionsCreated      uint64 `json:"sessions_created"`
	ContinuationAttempts uint64 `json:"continuation_attempts"`
}

func (e *Engine) conversationSnapshotLocked() ConversationSnapshot {
	retained := 0
	now := e.policy.now()
	for _, item := range e.conversations {
		current := e.entries[item.accountID]
		if now.Before(item.expires) && current == item.owner && !current.retired && current.account.Enabled {
			retained++
		}
	}
	return ConversationSnapshot{retained, maxConversations, int(conversationTTL / time.Second), e.sessionsCreated, e.continuationAttempts}
}

// Only opaque digests, an upstream session name and bounded file IDs survive
// a request. No prompt, response, cookie, JWT or attachment bytes are cached.
// Engine.mu owns this map and all its records. A continuation consumes its old
// prefix atomically with account acquisition, so a session has only one writer.
type conversation struct {
	key, principal, accountID, session string
	owner                              *entry
	fileIDs                            []string
	expires                            time.Time
	ambiguous                          bool
}

func digestValid(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateConversation(input contract.Request) error {
	if len(input.History) == 0 {
		return nil
	} // Internal single-turn callers.
	if len(input.History) > 33 || len(input.History)%2 != 1 || !digestValid(input.OptionsHash) {
		return contract.Fail(400, "INVALID_ARGUMENT", "Invalid Business conversation metadata")
	}
	for _, digest := range input.History {
		if !digestValid(digest) {
			return contract.Fail(400, "INVALID_ARGUMENT", "Invalid Business history digest")
		}
	}
	return nil
}

func conversationKey(principal string, selection contract.Selection, options string, history []string) string {
	scope := slices.Clone(selection.AccountIDs)
	slices.Sort(scope)
	data, _ := json.Marshal(struct {
		Principal, PublicID, ModelID, Options string
		Auto                                  bool
		Scope, History                        []string
	}{principal, selection.PublicID, selection.ModelID, options, selection.Auto, scope, history})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func unknownConversation() error {
	return contract.Fail(409, "FAILED_PRECONDITION", "Business history has no reusable completed prefix; start a new single-turn conversation")
}

func (e *Engine) pruneConversationsLocked() {
	now := e.policy.now()
	for key, item := range e.conversations {
		current := e.entries[item.accountID]
		if !now.Before(item.expires) || current != item.owner || current.retired || !current.account.Enabled {
			delete(e.conversations, key)
		}
	}
}

func (e *Engine) acquireConversation(ctx context.Context, selection contract.Selection, input contract.Request) (*lease, *conversation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneConversationsLocked()
	if len(input.History) <= 1 {
		a, err := e.acquireLocked(ctx, selection.AccountIDs)
		return a, nil, err
	}
	principal := contract.Principal(ctx)
	if principal == "" {
		return nil, nil, unknownConversation()
	}
	key := conversationKey(principal, selection, input.OptionsHash, input.History[:len(input.History)-1])
	previous := e.conversations[key]
	if previous == nil || previous.ambiguous {
		return nil, nil, unknownConversation()
	}
	a, err := e.acquireLocked(ctx, []string{previous.accountID})
	if err != nil {
		return nil, nil, err
	}
	// Even a failed auth/upload makes this continuation unavailable. In
	// particular no caller can retry an ambiguously submitted generation.
	delete(e.conversations, key)
	return a, previous, nil
}

func (e *Engine) completeConversation(caller context.Context, a *lease, selection contract.Selection, input contract.Request, session string, fileIDs []string, output []contract.Part) {
	principal := contract.Principal(caller)
	if principal == "" || len(input.History) == 0 || len(input.History) >= 33 || len(fileIDs) > 32 {
		return
	}
	e.mu.Lock()
	if !e.currentLeaseLocked(a) {
		e.mu.Unlock()
		return
	}
	epoch := e.conversationEpoch
	e.mu.Unlock()
	history := append(slices.Clone(input.History), contract.ModelHistoryDigest(output))
	key := conversationKey(principal, selection, input.OptionsHash, history)
	ownerHash := sha256.Sum256([]byte(principal))
	item := &conversation{key: key, principal: hex.EncodeToString(ownerHash[:]), accountID: a.account.ID, owner: a.owner, session: session,
		fileIDs: slices.Clone(fileIDs), expires: e.policy.now().Add(conversationTTL)}
	// Do not capture prompt/response buffers in the deferred delivery callback.
	contract.OnDelivery(caller, func(delivered bool) {
		if !delivered {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.ctx.Err() != nil || e.suspended || caller.Err() != nil || epoch != e.conversationEpoch {
			return
		}
		current := e.entries[item.accountID]
		if current != item.owner || current.retired || !current.account.Enabled || !sameCredentialVersion(current.account, a.account) {
			return
		}
		e.pruneConversationsLocked()
		if e.conversations == nil {
			e.conversations = map[string]*conversation{}
		}
		if saved := e.conversations[key]; saved != nil {
			// Identical public prefixes can come from two independent sessions,
			// possibly on different accounts. Do not guess which one the caller
			// meant or silently replace its associated upstream history.
			if saved.session != item.session || saved.owner != item.owner {
				saved.ambiguous = true
			}
			return
		}
		// LRU by fixed completion expiry, with independent principal admission.
		for {
			owned := 0
			var oldest, oldestOwned *conversation
			for _, saved := range e.conversations {
				if oldest == nil || saved.expires.Before(oldest.expires) {
					oldest = saved
				}
				if saved.principal == item.principal {
					owned++
					if oldestOwned == nil || saved.expires.Before(oldestOwned.expires) {
						oldestOwned = saved
					}
				}
			}
			if owned >= maxConversationsPerPrincipal {
				delete(e.conversations, oldestOwned.key)
				continue
			}
			if len(e.conversations) >= maxConversations {
				delete(e.conversations, oldest.key)
				continue
			}
			break
		}
		e.conversations[key] = item
	})
}

func appendContextFileIDs(previous, current []string) ([]string, error) {
	result := slices.Clone(previous)
	for _, id := range current {
		if id == "" || len(id) > 2048 || strings.ContainsAny(id, "\r\n") {
			return nil, contract.Fail(502, "DATA_LOSS", "Invalid Business context file ID")
		}
		if !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	if len(result) > 32 {
		return nil, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business conversation exceeds 32 context files")
	}
	return result, nil
}
