package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type conversationAssist struct {
	Session string   `json:"session"`
	FileIDs []string `json:"fileIds"`
	Query   struct {
		Parts []map[string]string `json:"parts"`
	} `json:"query"`
}

type conversationFixture struct {
	mu                       sync.Mutex
	calls, sessions, uploads int
	assists                  []conversationAssist
	uploadSessions           []string
	assistHook               func(*http.Request, int) (*http.Response, error)
}

func (f *conversationFixture) transport(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if r.URL.Path == "/auth/getoxsrf" {
		return reply(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
	}
	var body struct {
		Assist conversationAssist `json:"streamAssistRequest"`
		Upload struct {
			Name string `json:"name"`
		} `json:"addContextFileRequest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.mu.Lock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/widgetCreateSession"):
		f.sessions++
		n := f.sessions
		f.mu.Unlock()
		return reply(200, fmt.Sprintf(`{"session":{"name":"sessions/conversation-%d"}}`, n)), nil
	case strings.HasSuffix(r.URL.Path, "/widgetAddContextFile"):
		f.uploads++
		n := f.uploads
		f.uploadSessions = append(f.uploadSessions, body.Upload.Name)
		f.mu.Unlock()
		return reply(200, fmt.Sprintf(`{"addContextFileResponse":{"fileId":"context-%d"}}`, n)), nil
	case strings.HasSuffix(r.URL.Path, "/widgetStreamAssist"):
		f.assists = append(f.assists, body.Assist)
		n, hook := len(f.assists), f.assistHook
		f.mu.Unlock()
		if hook != nil {
			return hook(r, n)
		}
		return reply(200, stream), nil
	default:
		f.mu.Unlock()
		return nil, errors.New("unexpected fixture action")
	}
}

func conversationEngine(t *testing.T) (*Engine, *conversationFixture) {
	t.Helper()
	f := &conversationFixture{}
	e := New(context.Background(), []vault.Account{account()}, 2, time.Minute, roundTripFunc(f.transport))
	t.Cleanup(e.Close)
	return e, f
}

func conversationInput(text string) contract.Request {
	options := sha256.Sum256([]byte("synthetic-options"))
	return contract.Request{
		QueryParts:  []map[string]string{{"text": text}},
		History:     []string{contract.HistoryDigest("user", []contract.HistoryPart{{Text: &text}})},
		OptionsHash: hex.EncodeToString(options[:]),
	}
}

func conversationNext(previous contract.Request, output []contract.Part, text string) contract.Request {
	next := conversationInput(text)
	next.OptionsHash = previous.OptionsHash
	next.History = append(slices.Clone(previous.History), contract.ModelHistoryDigest(output), next.History[0])
	return next
}

func conversationSelection() contract.Selection {
	return contract.Selection{PublicID: "business-fixture", ModelID: "observed-fixture", AccountIDs: []string{account().ID}}
}

func conversationGenerate(t *testing.T, e *Engine, principal string, selection contract.Selection, input contract.Request, delivered bool) ([]contract.Part, error) {
	t.Helper()
	ctx, finish := contract.WithDelivery(contract.WithPrincipal(context.Background(), principal))
	var parts []contract.Part
	err := e.Generate(ctx, selection, input, func(event contract.Event) error {
		parts = append(parts, event.Parts...)
		return nil
	})
	finish(delivered && err == nil)
	return parts, err
}

func TestConversationContinuesOnlyDeliveredExactPrefixWithoutReplay(t *testing.T) {
	e, f := conversationEngine(t)
	first := conversationInput("first private question")
	selection := conversationSelection()
	ctx, finish := contract.WithDelivery(contract.WithPrincipal(context.Background(), "principal-a"))
	var output []contract.Part
	if err := e.Generate(ctx, selection, first, func(event contract.Event) error { output = append(output, event.Parts...); return nil }); err != nil {
		t.Fatal(err)
	}
	next := conversationNext(first, output, "second question only")
	before := f.calls
	_, err := conversationGenerate(t, e, "principal-a", selection, next, true)
	requireFailure(t, err, 409)
	if f.calls != before {
		t.Fatal("unacknowledged response was reused")
	}
	finish(true)
	second, err := conversationGenerate(t, e, "principal-a", selection, next, true)
	if err != nil {
		t.Fatal(err)
	}
	third := conversationNext(next, second, "third question")
	if _, err := conversationGenerate(t, e, "principal-a", selection, third, true); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 1 || f.uploads != 0 || len(f.assists) != 3 {
		t.Fatal("historical generation/session replay", f.sessions, f.uploads, len(f.assists))
	}
	for i, want := range []string{"first private question", "second question only", "third question"} {
		got := f.assists[i]
		if got.Session != "sessions/conversation-1" || len(got.Query.Parts) != 1 || got.Query.Parts[0]["text"] != want {
			t.Fatal("session or current query changed", got)
		}
	}
	before = f.calls
	_, err = conversationGenerate(t, e, "principal-a", selection, next, true)
	requireFailure(t, err, 409)
	if f.calls != before {
		t.Fatal("consumed prefix was replayed")
	}
}

func TestConversationOwnerModelOptionsHistoryEpochAndTTLIsolation(t *testing.T) {
	for _, name := range []string{"principal", "public-model", "upstream-model", "auto", "options", "scope", "user-history", "model-history", "epoch", "disable", "delete", "ttl"} {
		t.Run(name, func(t *testing.T) {
			e, f := conversationEngine(t)
			now := time.Now()
			e.policy.now = func() time.Time { return now }
			first, selection := conversationInput("private first"), conversationSelection()
			output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
			if err != nil {
				t.Fatal(err)
			}
			next, principal := conversationNext(first, output, "next"), "principal-a"
			switch name {
			case "principal":
				principal = "principal-b"
			case "public-model":
				selection.PublicID = "business-other"
			case "upstream-model":
				selection.ModelID = "another-model"
			case "auto":
				selection.Auto = true
			case "options":
				next.OptionsHash = strings.Repeat("1", 64)
			case "scope":
				selection.AccountIDs = nil
			case "user-history":
				next.History[0] = strings.Repeat("2", 64)
			case "model-history":
				next.History[1] = strings.Repeat("3", 64)
			case "epoch":
				changed := account()
				changed.Epoch++
				e.Reconcile([]vault.Account{changed}, 2, time.Minute)
			case "disable":
				changed := account()
				changed.Enabled = false
				e.Reconcile([]vault.Account{changed}, 2, time.Minute)
			case "delete":
				e.Reconcile(nil, 2, time.Minute)
			case "ttl":
				now = now.Add(conversationTTL)
			}
			before := f.calls
			_, err = conversationGenerate(t, e, principal, selection, next, true)
			requireFailure(t, err, 409)
			if f.calls != before {
				t.Fatal("isolated prefix reached authentication or generation")
			}
		})
	}
}

func TestConversationEquivalentScopeAndLabelEditKeepPrefix(t *testing.T) {
	e, f := conversationEngine(t)
	second := runtimeAccount("b")
	e.Reconcile([]vault.Account{account(), second}, 2, time.Minute)
	selection := conversationSelection()
	selection.AccountIDs = []string{account().ID, second.ID}
	first := conversationInput("first")
	output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
	if err != nil {
		t.Fatal(err)
	}
	changed := account()
	changed.Label = "new label"
	e.Reconcile([]vault.Account{changed, second}, 2, time.Minute)
	slices.Reverse(selection.AccountIDs)
	if _, err := conversationGenerate(t, e, "principal-a", selection, conversationNext(first, output, "next"), true); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 1 || len(f.assists) != 2 || f.assists[1].Session != f.assists[0].Session {
		t.Fatal("scope ordering or label change created a session")
	}
}

func TestConversationConcurrentForkHasOneUpstreamWriter(t *testing.T) {
	e, f := conversationEngine(t)
	first, selection := conversationInput("first"), conversationSelection()
	output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	f.assistHook = func(r *http.Request, n int) (*http.Response, error) {
		if n == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return reply(200, stream), nil
	}
	defer unblock.Do(func() { close(release) })
	next := conversationNext(first, output, "fork A")
	done := make(chan error, 1)
	go func() { _, err := conversationGenerate(t, e, "principal-a", selection, next, true); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("continuation did not enter transport")
	}
	fork := conversationNext(first, output, "fork B")
	_, err = conversationGenerate(t, e, "principal-a", selection, fork, true)
	requireFailure(t, err, 409)
	f.mu.Lock()
	count := len(f.assists)
	f.mu.Unlock()
	if count != 2 {
		t.Fatal("fork sent an extra generation", count)
	}
	// Let the only writer finish before testing that its prefix was consumed.
	unblock.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("single admitted writer failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish")
	}
	_, err = conversationGenerate(t, e, "principal-a", selection, fork, true)
	requireFailure(t, err, 409)
}

func TestConversationUnsuccessfulDeliveryAndFailedTurnCannotResume(t *testing.T) {
	for _, mode := range []string{"not-delivered", "no-delivery-contract", "emit-error", "upstream-error", "cancelled", "stale-before-delivery", "disabled-and-reenabled-before-delivery", "deleted-and-readded-before-delivery", "caller-cancelled-before-delivery", "suspended-and-resumed-before-delivery", "closed-before-delivery"} {
		t.Run(mode, func(t *testing.T) {
			e, f := conversationEngine(t)
			first, selection := conversationInput("first"), conversationSelection()
			base, cancel := context.WithCancel(contract.WithPrincipal(context.Background(), "principal-a"))
			defer cancel()
			ctx, finish := contract.WithDelivery(base)
			if mode == "no-delivery-contract" {
				ctx = base
			}
			if mode == "upstream-error" {
				f.assistHook = func(*http.Request, int) (*http.Response, error) { return reply(500, `{}`), nil }
			}
			var output []contract.Part
			err := e.Generate(ctx, selection, first, func(event contract.Event) error {
				output = append(output, event.Parts...)
				if mode == "emit-error" {
					return errors.New("downstream gone")
				}
				if mode == "cancelled" {
					cancel()
				}
				return nil
			})
			if mode == "emit-error" || mode == "upstream-error" || mode == "cancelled" {
				if err == nil {
					t.Fatal("failed turn returned success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "stale-before-delivery":
				changed := account()
				changed.Epoch++
				e.Reconcile([]vault.Account{changed}, 2, time.Minute)
			case "disabled-and-reenabled-before-delivery":
				changed := account()
				changed.Enabled = false
				e.Reconcile([]vault.Account{changed}, 2, time.Minute)
				e.Reconcile([]vault.Account{account()}, 2, time.Minute)
			case "deleted-and-readded-before-delivery":
				e.Reconcile(nil, 2, time.Minute)
				e.Reconcile([]vault.Account{account()}, 2, time.Minute)
			case "caller-cancelled-before-delivery":
				cancel()
			case "suspended-and-resumed-before-delivery":
				e.Suspend()
				e.Reconcile([]vault.Account{account()}, 2, time.Minute)
			case "closed-before-delivery":
				e.Close()
			}
			finish(mode != "not-delivered")
			e.mu.Lock()
			cached := len(e.conversations)
			e.mu.Unlock()
			if cached != 0 {
				t.Fatal("failed or undelivered turn entered conversation cache")
			}
			if mode != "closed-before-delivery" {
				before := f.calls
				_, err = conversationGenerate(t, e, "principal-a", selection, conversationNext(first, output, "next"), true)
				requireFailure(t, err, 409)
				if f.calls != before {
					t.Fatal("failed turn triggered a replay")
				}
			}
		})
	}
	// Once a valid prefix is consumed, an ambiguous upstream failure must not
	// put it back into the cache for a replay on retry.
	e, f := conversationEngine(t)
	first, selection := conversationInput("first"), conversationSelection()
	output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
	if err != nil {
		t.Fatal(err)
	}
	next := conversationNext(first, output, "second")
	f.assistHook = func(*http.Request, int) (*http.Response, error) { return reply(500, `{}`), nil }
	if _, err := conversationGenerate(t, e, "principal-a", selection, next, true); err == nil {
		t.Fatal("failed continuation returned success")
	}
	before := f.calls
	_, err = conversationGenerate(t, e, "principal-a", selection, next, true)
	requireFailure(t, err, 409)
	if f.calls != before {
		t.Fatal("ambiguous continuation replayed")
	}
}

func TestConversationCacheLimitsAndShutdown(t *testing.T) {
	e, _ := conversationEngine(t)
	now := time.Now()
	e.policy.now = func() time.Time { return now }
	selection := conversationSelection()
	var oldestInput, newestInput contract.Request
	var oldestOutput, newestOutput []contract.Part
	for i := 0; i <= maxConversationsPerPrincipal; i++ {
		now = now.Add(time.Millisecond)
		input := conversationInput(fmt.Sprintf("question-%d", i))
		output, err := conversationGenerate(t, e, "same-principal", selection, input, true)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldestInput, oldestOutput = input, output
		}
		newestInput, newestOutput = input, output
	}
	if len(e.conversations) != maxConversationsPerPrincipal {
		t.Fatal("per-principal cache exceeded its bound", len(e.conversations))
	}
	_, err := conversationGenerate(t, e, "same-principal", selection, conversationNext(oldestInput, oldestOutput, "evicted"), true)
	requireFailure(t, err, 409)
	if _, err := conversationGenerate(t, e, "same-principal", selection, conversationNext(newestInput, newestOutput, "retained"), true); err != nil {
		t.Fatal("recent conversation was evicted instead of the oldest", err)
	}
	for i := 0; i <= maxConversations; i++ {
		now = now.Add(time.Millisecond)
		if _, err := conversationGenerate(t, e, fmt.Sprintf("principal-%d", i), selection, conversationInput("same question"), true); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.conversations) != maxConversations {
		t.Fatal("global conversation cache exceeded its bound", len(e.conversations))
	}
	for _, item := range e.conversations {
		if strings.HasPrefix(item.principal, "principal-") || len(item.principal) != 64 || len(item.key) != 64 {
			t.Fatal("raw principal or invalid cache key retained")
		}
	}
	e.Suspend()
	if len(e.conversations) != 0 {
		t.Fatal("suspension retained reusable sessions")
	}
	e.Reconcile([]vault.Account{account()}, 2, time.Minute)
	if _, err := conversationGenerate(t, e, "principal-final", selection, conversationInput("new"), true); err != nil {
		t.Fatal(err)
	}
	e.Close()
	if len(e.conversations) != 0 {
		t.Fatal("shutdown retained reusable sessions")
	}
}

func TestConversationIdenticalIndependentPrefixesAreNotGuessed(t *testing.T) {
	e, f := conversationEngine(t)
	first, selection := conversationInput("identical question"), conversationSelection()
	output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conversationGenerate(t, e, "principal-a", selection, first, true); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 2 {
		t.Fatal("independent initial requests shared a session")
	}
	before := f.calls
	_, err = conversationGenerate(t, e, "principal-a", selection, conversationNext(first, output, "continue"), true)
	requireFailure(t, err, 409)
	if f.calls != before {
		t.Fatal("ambiguous prefix selected an arbitrary upstream session")
	}
}

func TestConversationFileIDsStayInsideOriginalSession(t *testing.T) {
	e, f := conversationEngine(t)
	first, selection := conversationInput("first attachment question"), conversationSelection()
	first.Attachments = []contract.Attachment{attachment()}
	output, err := conversationGenerate(t, e, "principal-a", selection, first, true)
	if err != nil {
		t.Fatal(err)
	}
	next := conversationNext(first, output, "question about the previous file")
	second, err := conversationGenerate(t, e, "principal-a", selection, next, true)
	if err != nil {
		t.Fatal(err)
	}
	third := conversationNext(next, second, "question about a new file")
	third.Attachments = []contract.Attachment{attachment()}
	if _, err := conversationGenerate(t, e, "principal-a", selection, third, true); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 1 || f.uploads != 2 || !slices.Equal(f.uploadSessions, []string{"sessions/conversation-1", "sessions/conversation-1"}) {
		t.Fatal("historical attachment uploaded again or session changed", f.sessions, f.uploads, f.uploadSessions)
	}
	for i, want := range [][]string{{"context-1"}, {"context-1"}, {"context-1", "context-2"}} {
		if !slices.Equal(f.assists[i].FileIDs, want) {
			t.Fatal("context IDs changed", i, f.assists[i].FileIDs)
		}
	}
	if _, err := conversationGenerate(t, e, "principal-a", selection, first, true); err != nil {
		t.Fatal(err)
	}
	last := f.assists[len(f.assists)-1]
	if f.sessions != 2 || f.uploads != 3 || last.Session != "sessions/conversation-2" || !slices.Equal(last.FileIDs, []string{"context-3"}) {
		t.Fatal("new conversation reused old context IDs", last)
	}
}
