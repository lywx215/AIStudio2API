package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementstore"
)

type g2Runtime struct {
	epoch                      uint64
	mu                         sync.Mutex
	acquired, released, native int
	failPublish                bool
	last                       c.AccountRecord
	entered                    chan struct{}
	unblock                    <-chan struct{}
	pool                       *aistudio.AccountPool
	publishEntered             chan struct{}
	publishContinue            <-chan struct{}
	nativeEntered              chan struct{}
}

func (r *g2Runtime) Epoch() uint64 { return r.epoch }
func (r *g2Runtime) Snapshot(context.Context) ([]c.NativeAccount, error) {
	return []c.NativeAccount{}, nil
}
func (r *g2Runtime) AcquireMutation(ctx context.Context, _ string, _ bool) (c.AccountMutationLease, error) {
	r.mu.Lock()
	r.acquired++
	r.mu.Unlock()
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.unblock != nil {
		select {
		case <-r.unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &g2MutationLease{runtime: r}, nil
}
func (r *g2Runtime) AcquireNativeMutation(context.Context, c.NativeLeaseProof) (c.AccountMutationLease, error) {
	r.mu.Lock()
	r.native++
	r.mu.Unlock()
	if r.nativeEntered != nil {
		select {
		case r.nativeEntered <- struct{}{}:
		default:
		}
	}
	return &g2MutationLease{runtime: r}, nil
}

type g2MutationLease struct {
	runtime *g2Runtime
	once    sync.Once
}

func (l *g2MutationLease) Epoch() uint64 { return l.runtime.epoch }
func (l *g2MutationLease) Publish(ctx context.Context, record c.AccountRecord) error {
	l.runtime.mu.Lock()
	l.runtime.last = record
	fail, pool, entered, unblock := l.runtime.failPublish, l.runtime.pool, l.runtime.publishEntered, l.runtime.publishContinue
	l.runtime.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if unblock != nil {
		select {
		case <-unblock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		return errors.New("synthetic publish failure")
	}
	if pool != nil {
		_, err := pool.PublishManagementAccount(record, true)
		return err
	}
	return nil
}

func TestManagementCommitPublicationSerializesNativeSuccessor(t *testing.T) {
	coordinator, store, runtime := g2CoordinatorFixture(t)
	impl := coordinator.(*managementAccountCoordinator)
	first, err := coordinator.Import(context.Background(), c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	continuePublish := make(chan struct{})
	defer func() {
		select {
		case <-continuePublish:
		default:
			close(continuePublish)
		}
	}()
	runtime.publishEntered = make(chan struct{}, 2)
	runtime.publishContinue = continuePublish
	runtime.nativeEntered = make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("replacement-a"), Expected: expectedAccount(first.Account), Enable: true})
		firstDone <- err
	}()
	select {
	case <-runtime.publishEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	committed, err := store.GetAccount(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := coordinator.PrepareCredentials(g2Credentials("replacement-b"))
	if err != nil {
		t.Fatal(err)
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		t.Fatal(err)
	}
	digest, _ := aistudio.CredentialDigest(state)
	files := impl.files()
	files.NewState = &state
	secondDone := make(chan error, 1)
	go func() {
		nativeCtx := c.WithNativeLease(ctx, g2NativeProof{id: first.Account.NativeID, epoch: runtime.epoch, active: true})
		_, err := coordinator.CommitNative(nativeCtx, c.CommitIntent{Kind: c.MutationReplace, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(committed), CredentialDigest: digest, CredentialChanged: true}, files)
		secondDone <- err
	}()
	select {
	case <-runtime.nativeEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The successor has entered the native runtime gate, but the first
	// publication still owns the credential/metadata commit window.
	time.Sleep(20 * time.Millisecond)
	current, err := store.GetAccount(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != 2 {
		t.Fatal("native successor committed before prior publication")
	}
	close(continuePublish)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	current, err = store.GetAccount(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != 3 {
		t.Fatal("successor did not commit")
	}
}

func TestManagementBindWaitingDoesNotBlockNativeDrainCompletion(t *testing.T) {
	coordinator, _, runtime := g2CoordinatorFixture(t)
	impl := coordinator.(*managementAccountCoordinator)
	first, err := coordinator.Import(context.Background(), c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	runtime.entered = make(chan struct{}, 1)
	runtime.unblock = unblock
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	managementDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Reload(ctx, c.StateCommand{AccountID: first.Account.AccountID})
		managementDone <- err
	}()
	select {
	case <-runtime.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	bindDone := make(chan error, 1)
	go func() { bindDone <- coordinator.BindRuntime(ctx, &g2Runtime{epoch: 2}) }()
	time.Sleep(20 * time.Millisecond)
	nativeCtx := c.WithNativeLease(ctx, g2NativeProof{id: first.Account.NativeID, epoch: 1, active: true})
	if _, err := coordinator.CommitNative(nativeCtx, c.CommitIntent{Kind: c.MutationState, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(first.Account), CredentialDigest: first.Account.CredentialDigest}, impl.files()); err != nil {
		t.Fatal("native completion blocked by pending bind", err)
	}
	close(unblock)
	if err := <-managementDone; err != nil {
		t.Fatal(err)
	}
	if err := <-bindDone; err != nil {
		t.Fatal(err)
	}
}
func (l *g2MutationLease) Release() error {
	l.once.Do(func() { l.runtime.mu.Lock(); l.runtime.released++; l.runtime.mu.Unlock() })
	return nil
}

func g2Credentials(value string) c.Credentials {
	cookies := make([]c.Cookie, 0, 3)
	for _, name := range []string{"SAPISID", "__Secure-1PAPISID", "__Secure-3PAPISID"} {
		cookies = append(cookies, c.Cookie{Name: name, Value: value, Domain: ".google.com", Path: "/", Expires: -1, Secure: true})
	}
	return c.Credentials{AccountName: "synthetic@example.test", Cookies: cookies, Origins: []c.StorageOrigin{}}
}
func g2CoordinatorFixture(t *testing.T) (c.AccountCoordinator, c.Store, *g2Runtime) {
	t.Helper()
	dir := t.TempDir()
	store, err := managementstore.Open(context.Background(), c.StoreOptions{Path: filepath.Join(dir, "metadata.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	coordinator, err := newManagementAccountCoordinator(c.AccountCoordinatorOptions{Store: store, AuthPaths: []string{filepath.Join(dir, "auth")}, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime := &g2Runtime{epoch: 1}
	if err := coordinator.BindRuntime(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	return coordinator, store, runtime
}

func TestManagementAccountLifecycleCASAndIdentity(t *testing.T) {
	coordinator, store, runtime := g2CoordinatorFixture(t)
	ctx := context.Background()
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Published || first.Account.CredentialVersion != 1 || first.Account.StateVersion != 1 {
		t.Fatalf("unexpected initial record: %+v", first.Account.Account)
	}
	if _, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("duplicate")}); c.PublicError(err).Code != c.CodeDuplicateAccount {
		t.Fatal("duplicate identity admitted", err)
	}
	archived, err := coordinator.Archive(ctx, c.StateCommand{AccountID: first.Account.AccountID, Expected: expectedAccount(first.Account)})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Account.Archived || archived.Account.Enabled {
		t.Fatal("archive state wrong")
	}
	archivedAgain, err := coordinator.Archive(ctx, c.StateCommand{AccountID: first.Account.AccountID})
	if err != nil {
		t.Fatal(err)
	}
	if archivedAgain.Account.StateVersion != archived.Account.StateVersion {
		t.Fatal("repeated archive changed state version")
	}
	if _, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("blocked"), Expected: expectedAccount(archived.Account), Enable: true}); c.PublicError(err).Code != c.CodeInvalidState {
		t.Fatal("archived replace accepted", err)
	}
	restored, err := coordinator.Restore(ctx, c.StateCommand{AccountID: first.Account.AccountID})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Account.Archived || restored.Account.Enabled || restored.Account.AccountID != first.Account.AccountID || restored.Account.Index != first.Account.Index {
		t.Fatal("restore changed identity or enabled account")
	}
	if _, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("stale"), Expected: expectedAccount(first.Account), Enable: true}); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("stale dual CAS accepted", err)
	}
	replaced, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("new"), Expected: expectedAccount(restored.Account), Enable: true})
	if err != nil {
		t.Fatal(err)
	}
	if !replaced.Account.Enabled || replaced.Account.CredentialVersion != 2 || replaced.Account.AccountID != first.Account.AccountID {
		t.Fatal("replace violated identity/version")
	}
	_, material, err := coordinator.SnapshotCredentials(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if material.NativeID != "synthetic@example.test" {
		t.Fatal("native identity mismatch")
	}
	current, err := store.GetAccount(ctx, first.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Versions != replaced.Account.Versions {
		t.Fatal("metadata/file publication diverged")
	}
	if runtime.acquired != runtime.released {
		t.Fatal("mutation lease leaked")
	}
}

func TestManagementPublishFailureRetainsOriginalReceipt(t *testing.T) {
	coordinator, store, runtime := g2CoordinatorFixture(t)
	ctx := context.Background()
	runtime.failPublish = true
	identity := &c.UploadIdentity{TaskID: "task_10000000-0000-4000-8000-000000000001", ItemIndex: 0, Kind: c.TaskImport}
	result, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("durable"), Operation: c.OperationContext{Upload: identity}})
	if c.PublicError(err).Code != c.CodePersistenceError || result.Published || result.Upload == nil {
		t.Fatal("committed publish failure lost evidence", err)
	}
	receipt, err := store.GetUploadReceipt(ctx, *identity)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || *receipt != *result.Upload {
		t.Fatal("original receipt was not persisted")
	}
	current, _, err := coordinator.SnapshotCredentials(ctx, result.Account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != 1 {
		t.Fatal("publication failure repeated write")
	}
	if runtime.released != 1 {
		t.Fatal("failed publication lease leaked")
	}
}

type g2NativeProof struct {
	id     string
	epoch  uint64
	active bool
}

func (p g2NativeProof) NativeLeaseAccountID() string { return p.id }
func (p g2NativeProof) NativeLeaseEpoch() uint64     { return p.epoch }
func (p g2NativeProof) NativeLeaseActive() bool      { return p.active }

func TestManagementNativeReusesLeaseAndRejectsOldEpoch(t *testing.T) {
	coordinator, _, runtime := g2CoordinatorFixture(t)
	ctx := context.Background()
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	impl := coordinator.(*managementAccountCoordinator)
	intent := c.CommitIntent{Kind: c.MutationState, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(first.Account), CredentialDigest: first.Account.CredentialDigest}
	ctx = c.WithNativeLease(ctx, g2NativeProof{id: first.Account.NativeID, epoch: 1, active: true})
	if _, err := coordinator.CommitNative(ctx, intent, impl.files()); err != nil {
		t.Fatal(err)
	}
	if runtime.acquired != 1 || runtime.native != 1 {
		t.Fatal("native mutation entered self drain")
	}
	ctx = c.WithNativeLease(context.Background(), g2NativeProof{id: first.Account.NativeID, epoch: 2, active: true})
	if _, err := coordinator.CommitNative(ctx, intent, impl.files()); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("old epoch committed", err)
	}
	if runtime.released != 2 {
		t.Fatal("native lease leaked")
	}
}

func TestManagementBindWaitsForOutstandingMutation(t *testing.T) {
	coordinator, _, runtime := g2CoordinatorFixture(t)
	unblock := make(chan struct{})
	runtime.unblock = unblock
	runtime.entered = make(chan struct{}, 1)
	committed := make(chan error, 1)
	go func() {
		_, err := coordinator.Import(context.Background(), c.ImportCommand{Credentials: g2Credentials("initial")})
		committed <- err
	}()
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("mutation did not start")
	}
	bound := make(chan error, 1)
	go func() { bound <- coordinator.BindRuntime(context.Background(), &g2Runtime{epoch: 2}) }()
	select {
	case err := <-bound:
		t.Fatal("generation switched under mutation", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(unblock)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := <-bound; err != nil {
		t.Fatal(err)
	}
	if runtime.released != 1 {
		t.Fatal("generation switch preceded lease release")
	}
}

func g2NativePoolFixture(t *testing.T) (c.AccountCoordinator, c.Store, *aistudio.AccountPool, *g2Runtime, c.AccountRecord) {
	t.Helper()
	coordinator, store, _ := g2CoordinatorFixture(t)
	result, err := coordinator.Import(context.Background(), c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	impl := coordinator.(*managementAccountCoordinator)
	nativeStore := aistudio.NewAccountStore(impl.paths...)
	accounts, err := nativeStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	pool := aistudio.NewAccountPool(accounts, 4)
	pool.SetNativeWriteCoordinator(coordinator, impl.paths, impl.journal)
	pool.SetManagementEpoch(2)
	runtime := &g2Runtime{epoch: 2, pool: pool}
	if err := coordinator.BindRuntime(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	return coordinator, store, pool, runtime, result.Account
}

func g2NativePair(t *testing.T, pool *aistudio.AccountPool, id string) (*aistudio.AccountLease, *aistudio.AccountLease) {
	t.Helper()
	old, err := pool.AcquireFor(context.Background(), aistudio.AccountSelection{AccountID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := old.Release(); err != nil {
			t.Error(err)
		}
	})
	time.Sleep(time.Millisecond)
	newer, err := pool.AcquireFor(context.Background(), aistudio.AccountSelection{AccountID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := newer.Release(); err != nil {
			t.Error(err)
		}
	})
	return old, newer
}

func TestManagementNativeAuthenticationTimeFence(t *testing.T) {
	for _, failureFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged-success-fences-old-failure", true: "new-success-clears-older-failure"}[failureFirst], func(t *testing.T) {
			_, store, pool, runtime, record := g2NativePoolFixture(t)
			older, newer := g2NativePair(t, pool, record.NativeID)
			if failureFirst {
				if err := older.MarkAuthenticationRequired("synthetic rejected credentials"); err != nil {
					t.Fatal(err)
				}
			}
			if err := newer.MarkAuthenticationValid(); err != nil {
				t.Fatal(err)
			}
			if !failureFirst {
				if err := older.MarkAuthenticationRequired("late old failure"); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.GetAccount(context.Background(), record.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Expired || current.DisabledReason != nil {
				t.Fatal("older authentication failure defeated newer success")
			}
			if !failureFirst && current.StateVersion != 1 {
				t.Fatal("unchanged authentication success changed state version")
			}
			if runtime.acquired != 0 || runtime.native != runtime.released {
				t.Fatal("native authentication self-drained or leaked lease")
			}
		})
	}
}

func TestManagementNativeConcurrentCookiesMergeLatest(t *testing.T) {
	coordinator, store, pool, runtime, record := g2NativePoolFixture(t)
	older, newer := g2NativePair(t, pool, record.NativeID)
	url := "https://aistudio.google.com/"
	now := time.Now()
	if err := newer.MergeSetCookieHeaders([]string{"ROTATED=newest; Path=/; Secure"}, url, now); err != nil {
		t.Fatal(err)
	}
	if err := older.MergeSetCookieHeaders([]string{"ROTATED=stale; Path=/; Secure", "OTHER=independent; Path=/; Secure"}, url, now); err != nil {
		t.Fatal(err)
	}
	current, material, err := coordinator.SnapshotCredentials(context.Background(), record.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		t.Fatal(err)
	}
	if value, _ := state.CookieValue("ROTATED", url, now); value != "newest" {
		t.Fatal("old response overwrote newer rotation")
	}
	if value, _ := state.CookieValue("OTHER", url, now); value != "independent" {
		t.Fatal("independent rotation was lost")
	}
	if current.CredentialVersion != 3 {
		t.Fatal("effective cookie writes did not advance persistent versions", current.CredentialVersion)
	}
	if runtime.acquired != 0 || runtime.native != 2 {
		t.Fatal("cookie writes self-drained")
	}
	got, err := store.GetAccount(context.Background(), record.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Versions != current.Versions {
		t.Fatal("runtime/file metadata split")
	}
}

func TestManagementNativeRefreshNetworkOutsideCommitGate(t *testing.T) {
	coordinator, _, pool, _, record := g2NativePoolFixture(t)
	older, newer := g2NativePair(t, pool, record.NativeID)
	err := older.RefreshStorageState(func(state *aistudio.StorageState) error {
		if err := newer.MergeSetCookieHeaders([]string{"DURING_NETWORK=keep; Path=/; Secure"}, "https://aistudio.google.com/", time.Now()); err != nil {
			return err
		}
		state.Cookies[0].Value = "stale-network-result"
		return nil
	}, func() (func(bool), error) { return func(bool) {}, nil })
	if c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("refresh ignored concurrent persistent version", err)
	}
	_, material, err := coordinator.SnapshotCredentials(context.Background(), record.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		t.Fatal(err)
	}
	if value, _ := state.CookieValue("DURING_NETWORK", "https://aistudio.google.com/", time.Now()); value != "keep" {
		t.Fatal("refresh overwrote concurrent file")
	}
}

func TestManagementNativeOldEpochDiscardsSuccessfulCookies(t *testing.T) {
	coordinator, store, pool, _, record := g2NativePoolFixture(t)
	lease, err := pool.AcquireFor(context.Background(), aistudio.AccountSelection{AccountID: record.NativeID})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if !lease.NativeLeaseActive() || lease.NativeLeaseEpoch() != 2 {
		t.Fatal("lease proof not bound to pool epoch")
	}
	if err := coordinator.BindRuntime(context.Background(), &g2Runtime{epoch: 3}); err != nil {
		t.Fatal(err)
	}
	if err := lease.MergeSetCookieHeaders([]string{"STALE_GENERATION=discard; Path=/; Secure"}, "https://aistudio.google.com/", time.Now()); err != nil {
		t.Fatal("successful request failed because generation changed", err)
	}
	current, err := store.GetAccount(context.Background(), record.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CredentialVersion != 1 {
		t.Fatal("old generation wrote credentials")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if lease.NativeLeaseActive() {
		t.Fatal("released lease retained active proof")
	}
}

func TestManagementHardDeleteTombstoneWithBrowserCache(t *testing.T) {
	coordinator, store, _ := g2CoordinatorFixture(t)
	impl := coordinator.(*managementAccountCoordinator)
	ctx := context.Background()
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	nativeStore := aistudio.NewAccountStore(impl.paths...)
	nativeStore.SetNativeWriteCoordinator(coordinator, impl.journal)
	accounts, err := nativeStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatal("native account missing")
	}
	cache := filepath.Join(accounts[0].Directory, "camoufox-cache", "nested")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "synthetic"), []byte("synthetic browser cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := nativeStore.Delete(accounts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(accounts[0].Directory); !os.IsNotExist(err) {
		t.Fatal("hard delete left original directory")
	}
	if _, err := store.GetAccount(ctx, first.Account.AccountID); c.PublicError(err).Code != c.CodeNotFound {
		t.Fatal("deleted identity remained public", err)
	}
	if _, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("duplicate")}); c.PublicError(err).Code != c.CodeDuplicateAccount {
		t.Fatal("delete tombstone allowed identity reuse", err)
	}
	credentials := g2Credentials("next")
	credentials.AccountName = "second-synthetic@example.test"
	next, err := coordinator.Import(ctx, c.ImportCommand{Credentials: credentials})
	if err != nil {
		t.Fatal(err)
	}
	if next.Account.Index <= first.Account.Index {
		t.Fatal("hard delete reused persistent index")
	}
}
