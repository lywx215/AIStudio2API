package aistudio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// Hooks are installed before a generation is published and never changed on a
// live pool/store. The process coordinator outlives every such generation.
func (s *AccountStore) SetNativeWriteCoordinator(writer contract.NativeWriteCoordinator, journal string) {
	s.writer, s.journalDir = writer, journal
}
func (s *AccountStore) NativeWritesCoordinated() bool { return s != nil && s.writer != nil }
func (s *AccountStore) AuthPaths() []string           { return append([]string(nil), s.paths...) }
func (p *AccountPool) SetNativeWriteCoordinator(writer contract.NativeWriteCoordinator, paths []string, journal string) {
	p.writer, p.authPaths, p.journalDir = writer, append([]string(nil), paths...), journal
}
func (p *AccountPool) SetManagementEpoch(epoch uint64)   { p.managementEpoch.Store(epoch) }
func NormalizeAccountEmail(email string) (string, error) { return normalizeAccountEmail(email) }

// BelongsToPool is safe while the caller holds this lease's operation lock; it
// intentionally does not acquire that same lock from the runtime adapter.
func (l *AccountLease) BelongsToPool(pool *AccountPool) bool { return l != nil && l.pool == pool }
func (l *AccountLease) NativeLeaseAccountID() string {
	if l == nil || l.account == nil {
		return ""
	}
	return l.account.ID
}
func (l *AccountLease) NativeLeaseEpoch() uint64 {
	if l == nil {
		return 0
	}
	return l.nativeEpoch
}
func (l *AccountLease) NativeLeaseActive() bool {
	return l != nil && !l.nativeReleased.Load() && l.nativeEpoch != 0
}

func (l *AccountLease) captureNative(ctx context.Context) error {
	if l.pool.writer == nil {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		record, err := l.pool.writer.SnapshotNative(ctx, l.account.ID)
		if err != nil {
			return err
		}
		state, err := LoadStorageState(l.account.StoragePath)
		if err != nil {
			return err
		}
		digest, err := CredentialDigest(state)
		if err != nil {
			return err
		}
		if digest != record.CredentialDigest {
			continue
		}
		l.nativeSnapshot, l.nativeSnapshotValid = record, true
		l.nativeBaseState = state
		l.nativeEpoch = l.pool.managementEpoch.Load()
		return nil
	}
	return contract.NewError(contract.CodeVersionConflict)
}

func nativeExpected(record contract.AccountRecord) contract.ExpectedVersions {
	credential, state := record.CredentialVersion, record.StateVersion
	return contract.ExpectedVersions{Credential: &credential, State: &state}
}

func (l *AccountLease) nativeFiles() *AccountFileParticipant {
	return &AccountFileParticipant{AuthPaths: l.pool.authPaths, JournalDir: l.pool.journalDir}
}

func (l *AccountLease) nativeIntent(kind contract.MutationKind) (contract.CommitIntent, error) {
	if l.released || !l.nativeSnapshotValid {
		return contract.CommitIntent{}, fmt.Errorf("native credential snapshot missing or released")
	}
	r := l.nativeSnapshot
	return contract.CommitIntent{Kind: kind, AccountID: r.AccountID, NativeID: r.NativeID, Expected: nativeExpected(r), CredentialDigest: r.CredentialDigest}, nil
}

func (l *AccountLease) commitNative(intent contract.CommitIntent, files contract.AccountCommitParticipant) (contract.AccountCommitResult, error) {
	ctx := contract.WithNativeLease(ContextWithAccountLease(context.Background(), l), l)
	result, err := l.pool.writer.CommitNative(ctx, intent, files)
	if result.Account.AccountID != "" {
		l.nativeSnapshot, l.nativeSnapshotValid = result.Account, true
		l.pool.mu.Lock()
		l.authGeneration = l.account.authGeneration
		l.pool.mu.Unlock()
		if state, loadErr := LoadStorageState(l.account.StoragePath); loadErr == nil {
			l.nativeBaseState = state
		}
	}
	return result, err
}

func (l *AccountLease) saveNativeState(state StorageState, kind contract.MutationKind, opportunistic bool) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	return l.saveNativeStateLocked(state, kind, opportunistic)
}

func (s *AccountStore) CreateWithFingerprint(config AccountConfig, state StorageState, fingerprint []byte) (*Account, *AccountPublishLease, error) {
	if s.writer == nil {
		return nil, nil, fmt.Errorf("fingerprint transaction requires management coordinator")
	}
	return s.createCoordinatedWithFingerprint(config, state, fingerprint)
}

func (l *AccountLease) SaveStorageStateWithFingerprint(state StorageState, fingerprint []byte) error {
	if l == nil || l.pool == nil || l.pool.writer == nil {
		return fmt.Errorf("fingerprint transaction requires management coordinator")
	}
	l.operation.Lock()
	defer l.operation.Unlock()
	intent, err := l.nativeIntent(contract.MutationReplace)
	if err != nil {
		return err
	}
	digest, err := CredentialDigest(state)
	if err != nil {
		return err
	}
	intent.CredentialChanged = true
	intent.CredentialDigest = digest
	next := l.nativeSnapshot.AccountState
	next.Expired = false
	if next.DisabledReason != nil && *next.DisabledReason == "auth_required" {
		next.DisabledReason = nil
		next.DisabledAt = nil
		next.DisabledStatus = nil
	}
	intent.State = &next
	files := l.nativeFiles()
	files.NewState = &state
	if len(fingerprint) > 0 {
		files.ExtraFiles = map[string][]byte{"camoufox-fingerprint.json": fingerprint}
	}
	result, err := l.commitNative(intent, files)
	if result.Account.AccountID != "" {
		l.advanceNativeLineage()
	}
	return err
}

func (l *AccountLease) saveNativeStateLocked(state StorageState, kind contract.MutationKind, opportunistic bool) error {
	intent, err := l.nativeIntent(kind)
	if err != nil {
		return err
	}
	digest, err := CredentialDigest(state)
	if err != nil {
		return err
	}
	intent.CredentialChanged = digest != l.nativeSnapshot.CredentialDigest
	intent.CredentialDigest = digest
	if !intent.CredentialChanged && kind == contract.MutationRefresh {
		return nil
	}
	files := l.nativeFiles()
	files.NewState = &state
	result, err := l.commitNative(intent, files)
	if result.Account.AccountID != "" && kind == contract.MutationReplace {
		l.advanceNativeLineage()
	}
	if opportunistic && contract.PublicError(err).Code == contract.CodeVersionConflict {
		return nil
	}
	return err
}

func (l *AccountLease) refreshNativeState(update func(*StorageState) error, prepare func() (func(bool), error)) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	// A dedicated refresh mutex deduplicates network refreshes without holding
	// storageMu, pool.mu, the coordinator gate, or a database transaction.
	l.account.authRefreshMu.Lock()
	defer l.account.authRefreshMu.Unlock()
	if _, err := l.nativeIntent(contract.MutationRefresh); err != nil {
		return err
	}
	l.pool.mu.Lock()
	generation := l.account.authGeneration
	l.pool.mu.Unlock()
	if l.authGeneration != generation {
		l.authGeneration = generation
		return l.captureNative(context.Background())
	}
	// Load and capture versions BEFORE invoking network/browser update. No
	// storageMu, pool.mu, management gate or SQLite transaction spans update.
	if err := l.captureNative(context.Background()); err != nil {
		return err
	}
	state := l.nativeBaseState
	state.Cookies = append([]StateCookie(nil), state.Cookies...)
	if err := update(&state); err != nil {
		return err
	}
	finish, err := prepare()
	if err != nil {
		return err
	}
	if finish == nil {
		return fmt.Errorf("native runtime preparation returned no completion")
	}
	intent, err := l.nativeIntent(contract.MutationRefresh)
	if err != nil {
		finish(false)
		return err
	}
	digest, err := CredentialDigest(state)
	if err != nil {
		finish(false)
		return err
	}
	intent.CredentialDigest = digest
	intent.CredentialChanged = digest != l.nativeSnapshot.CredentialDigest
	files := l.nativeFiles()
	files.NewState = &state
	result, err := l.commitNative(intent, files)
	if result.Account.AccountID != "" {
		l.advanceNativeLineage()
	}
	finish(result.Account.AccountID != "")
	return err
}

func (l *AccountLease) advanceNativeLineage() {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	l.account.authGeneration++
	l.authGeneration = l.account.authGeneration
	l.account.authCheckedAt = time.Time{}
	l.account.modelAccessGeneration++
	l.modelAccessGeneration = l.account.modelAccessGeneration
	l.account.runtime.ModelAccess = make(map[string]ModelAccess)
	l.pool.notifyLocked()
}

func (l *AccountLease) saveNativeConfig(value AccountConfig) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	intent, err := l.nativeIntent(contract.MutationState)
	if err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Label != l.account.ID {
		return fmt.Errorf("native identity cannot change")
	}
	state := l.nativeSnapshot.AccountState
	if value.Enabled != state.Enabled {
		state.Enabled = value.Enabled
		if !value.Enabled {
			reason := "manual"
			now := contract.NewTimestamp(time.Now())
			state.DisabledReason = &reason
			state.DisabledStatus = nil
			state.DisabledAt = &now
		} else {
			if state.Archived {
				return contract.NewError(contract.CodeInvalidState)
			}
			state.DisabledReason = nil
			state.DisabledStatus = nil
			state.DisabledAt = nil
		}
	}
	intent.State = &state
	files := l.nativeFiles()
	files.NewConfig = &value
	_, err = l.commitNative(intent, files)
	return err
}

func (l *AccountLease) markNativeAuthentication(required bool, checkedAt time.Time) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	l.account.authStateMu.Lock()
	defer l.account.authStateMu.Unlock()
	intent, err := l.nativeIntent(contract.MutationState)
	if err != nil {
		return err
	}
	l.pool.mu.Lock()
	stale := l.authGeneration != l.account.authGeneration || checkedAt.Before(l.account.authCheckedAt)
	if !stale {
		l.account.authCheckedAt = checkedAt
	}
	l.pool.mu.Unlock()
	if stale {
		return nil
	}
	// An earlier authentication observation may have committed since this
	// request began. Rebase only authentication metadata in the same persistent
	// credential version; manual/archive/enabled changes are never cleared.
	latest, lookupErr := l.pool.writer.SnapshotNative(context.Background(), l.account.ID)
	if lookupErr != nil {
		if nativeStaleWrite(lookupErr) {
			return nil
		}
		return lookupErr
	}
	if !sameNativeAuthenticationState(l.nativeSnapshot, latest) {
		return nil
	}
	l.nativeSnapshot = latest
	intent.Expected = nativeExpected(latest)
	intent.CredentialDigest = latest.CredentialDigest
	state := latest.AccountState
	if state.Expired == required {
		return nil
	}
	state.Expired = required
	if state.Enabled && required {
		reason := "auth_required"
		now := contract.NewTimestamp(checkedAt)
		state.DisabledReason = &reason
		state.DisabledAt = &now
		status := 401
		state.DisabledStatus = &status
	}
	if state.Enabled && !required && state.DisabledReason != nil && *state.DisabledReason == "auth_required" {
		state.DisabledReason = nil
		state.DisabledStatus = nil
		state.DisabledAt = nil
	}
	intent.State = &state
	_, err = l.commitNative(intent, l.nativeFiles())
	if nativeStaleWrite(err) {
		return nil
	}
	return err
}

func sameNativeAuthenticationState(before, after contract.AccountRecord) bool {
	if before.CredentialVersion != after.CredentialVersion || after.Deleted {
		return false
	}
	strip := func(state contract.AccountState) contract.AccountState {
		state.Expired = false
		if state.DisabledReason != nil && *state.DisabledReason == "auth_required" {
			state.DisabledReason = nil
			state.DisabledStatus = nil
			state.DisabledAt = nil
		}
		return state
	}
	return reflect.DeepEqual(strip(before.AccountState), strip(after.AccountState))
}

// Ordinary successful responses may rotate cookies concurrently. Only merge
// into the same in-memory credential lineage AND unchanged persistent state.
// A bounded CAS retry reads the latest file and never repeats a network call.
func (l *AccountLease) mergeNativeCookies(cookies []StateCookie, headers []string, url string, now time.Time) error {
	l.operation.Lock()
	defer l.operation.Unlock()
	if _, err := l.nativeIntent(contract.MutationRefresh); err != nil {
		return err
	}
	candidate := l.nativeBaseState
	candidate.Cookies = append([]StateCookie(nil), l.nativeBaseState.Cookies...)
	if cookies != nil {
		candidate.Cookies = append([]StateCookie(nil), cookies...)
	} else if err := candidate.MergeSetCookieHeaders(headers, url, now); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		l.pool.mu.Lock()
		lineage := l.account.authGeneration
		l.pool.mu.Unlock()
		if lineage != l.authGeneration {
			return nil
		}
		latest, err := l.pool.writer.SnapshotNative(context.Background(), l.account.ID)
		if err != nil {
			if nativeStaleWrite(err) {
				return nil
			}
			return err
		}
		if latest.Deleted || latest.Archived || latest.StateVersion != l.nativeSnapshot.StateVersion {
			return nil
		}
		state, err := LoadStorageState(l.account.StoragePath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		digest, err := CredentialDigest(state)
		if err != nil {
			return err
		}
		if digest != latest.CredentialDigest {
			continue
		}
		state.Cookies = mergeCookieChanges(l.nativeBaseState.Cookies, candidate.Cookies, state.Cookies)
		l.nativeSnapshot = latest
		err = l.saveNativeStateLocked(state, contract.MutationRefresh, false)
		if nativeStaleWrite(err) {
			continue
		}
		return err
	}
	return nil
}

func nativeStaleWrite(err error) bool {
	if err == nil {
		return false
	}
	switch contract.PublicError(err).Code {
	case contract.CodeVersionConflict, contract.CodeInvalidState, contract.CodeNotFound:
		return true
	}
	return false
}

func cookieIdentity(cookie StateCookie) string {
	return cookie.Name + "\x00" + strings.ToLower(strings.TrimPrefix(cookie.Domain, ".")) + "\x00" + cookie.Path + "\x00" + cookie.PartitionKey
}

// Three-way merge prevents an older response/browser cookie snapshot from
// deleting or replacing a cookie changed by a newer response in this lineage.
func mergeCookieChanges(base, incoming, latest []StateCookie) []StateCookie {
	before := make(map[string]StateCookie, len(base))
	after := make(map[string]StateCookie, len(incoming))
	current := make(map[string]StateCookie, len(latest))
	for _, cookie := range base {
		before[cookieIdentity(cookie)] = cookie
	}
	for _, cookie := range incoming {
		after[cookieIdentity(cookie)] = cookie
	}
	for _, cookie := range latest {
		current[cookieIdentity(cookie)] = cookie
	}
	keys := make(map[string]bool, len(base)+len(incoming))
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for key := range keys {
		old, oldOK := before[key]
		next, nextOK := after[key]
		if oldOK == nextOK && reflect.DeepEqual(old, next) {
			continue
		}
		now, nowOK := current[key]
		if oldOK != nowOK || !reflect.DeepEqual(old, now) {
			continue
		}
		if nextOK {
			current[key] = next
		} else {
			delete(current, key)
		}
	}
	result := make([]StateCookie, 0, len(current))
	seen := make(map[string]bool, len(current))
	for _, cookie := range latest {
		key := cookieIdentity(cookie)
		if next, ok := current[key]; ok {
			result = append(result, next)
			seen[key] = true
		}
	}
	for _, cookie := range incoming {
		key := cookieIdentity(cookie)
		if next, ok := current[key]; ok && !seen[key] {
			result = append(result, next)
			seen[key] = true
		}
	}
	return result
}

func (s *AccountStore) createCoordinated(config AccountConfig, state StorageState) (*Account, *AccountPublishLease, error) {
	return s.createCoordinatedWithFingerprint(config, state, nil)
}

func (s *AccountStore) createCoordinatedWithFingerprint(config AccountConfig, state StorageState, fingerprint []byte) (*Account, *AccountPublishLease, error) {
	id, err := accountEmailID(config, state)
	if err != nil {
		return nil, nil, err
	}
	config.Label = id
	if _, err := s.writer.SnapshotNative(context.Background(), id); err == nil {
		return nil, nil, contract.NewError(contract.CodeDuplicateAccount)
	} else if contract.PublicError(err).Code != contract.CodeNotFound {
		return nil, nil, err
	}
	digest, err := CredentialDigest(state)
	if err != nil {
		return nil, nil, err
	}
	files := &AccountFileParticipant{AuthPaths: s.paths, JournalDir: s.journalDir, NewState: &state, NewConfig: &config}
	if len(fingerprint) > 0 {
		files.ExtraFiles = map[string][]byte{"camoufox-fingerprint.json": fingerprint}
	}
	name := id
	next := contract.AccountState{Enabled: config.Enabled}
	result, err := s.writer.CommitNative(context.Background(), contract.CommitIntent{Kind: contract.MutationImport, NativeID: id, AccountName: &name, State: &next, CredentialChanged: true, CredentialDigest: digest}, files)
	if err != nil {
		return nil, nil, err
	}
	if !result.Published {
		return nil, nil, contract.NewError(contract.CodePersistenceError)
	}
	directory, err := AccountDirectory(s.paths, id, false)
	if err != nil {
		return nil, nil, err
	}
	account, err := loadAccount(directory)
	return account, nil, err
}

func (s *AccountStore) deleteCoordinated(account *Account) error {
	if account == nil {
		return fmt.Errorf("account missing")
	}
	record, err := s.writer.SnapshotNative(context.Background(), account.ID)
	if err != nil {
		return err
	}
	files := &AccountFileParticipant{AuthPaths: s.paths, JournalDir: s.journalDir}
	_, err = s.writer.CommitNative(context.Background(), contract.CommitIntent{Kind: contract.MutationDelete, AccountID: record.AccountID, NativeID: record.NativeID, Expected: nativeExpected(record), CredentialDigest: record.CredentialDigest}, files)
	return err
}

// PublishManagementAccount is local publication while the runtime mutation lease
// keeps request admission closed. Native Cookie writes preserve auth lineage;
// explicit replacement invalidates stale requests even for equal credentials.
func (p *AccountPool) PublishManagementAccount(record contract.AccountRecord, preserveLineage bool) (*Account, error) {
	p.mu.Lock()
	account := p.byID[record.NativeID]
	p.mu.Unlock()
	if record.Deleted {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.byID, record.NativeID)
		for i, a := range p.accounts {
			if a == account {
				p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
				break
			}
		}
		for resource, owner := range p.resources {
			if owner == record.NativeID {
				delete(p.resources, resource)
			}
		}
		p.notifyLocked()
		return account, nil
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	config, err := readAccountConfig(account.ConfigPath)
	if err != nil {
		return nil, err
	}
	state, err := LoadStorageState(account.StoragePath)
	if err != nil {
		return nil, err
	}
	digest, err := CredentialDigest(state)
	if err != nil || digest != record.CredentialDigest {
		return nil, fmt.Errorf("published credential digest mismatch")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	account.Config = config
	account.StorageState = state
	account.Config.Enabled = record.Enabled && !record.Archived
	if !preserveLineage {
		account.authGeneration++
		account.authCheckedAt = time.Time{}
		account.modelAccessGeneration++
		account.runtime.ModelAccess = make(map[string]ModelAccess)
	}
	switch {
	case !account.Config.Enabled:
		account.State = AccountDisabled
	case record.Expired:
		account.State = AccountAuthRequired
	default:
		account.State = AccountReady
	}
	account.stateMessage = ""
	p.notifyLocked()
	return account, nil
}

// LoadAccountDirectory is an explicit-path composition helper; it reads no
// global config and never admits the returned account to production itself.
func LoadAccountDirectory(directory string) (*Account, error) {
	return loadAccount(filepath.Clean(directory))
}

var _ contract.NativeLeaseProof = (*AccountLease)(nil)
