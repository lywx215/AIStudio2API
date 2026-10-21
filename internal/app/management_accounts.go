package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type managementAccountCoordinator struct {
	store     contract.AccountMetadataStore
	paths     []string
	journal   string
	clock     contract.Clock
	newID     contract.IDGenerator
	bind      sync.RWMutex
	gate      sync.Mutex
	runtime   contract.AccountRuntime
	epochs    map[uint64]bool
	recovered bool
}

func newManagementAccountCoordinator(options contract.AccountCoordinatorOptions) (contract.AccountCoordinator, error) {
	if options.Store == nil || len(options.AuthPaths) == 0 || options.JournalDir == "" {
		return nil, fmt.Errorf("management account paths and store are required")
	}
	c := &managementAccountCoordinator{store: options.Store, paths: append([]string(nil), options.AuthPaths...), journal: options.JournalDir, clock: options.Clock, newID: options.NewID, epochs: make(map[uint64]bool)}
	if c.clock == nil {
		c.clock = time.Now
	}
	if c.newID == nil {
		c.newID = contract.NewID
	}
	return c, nil
}

func (c *managementAccountCoordinator) files() *aistudio.AccountFileParticipant {
	return &aistudio.AccountFileParticipant{AuthPaths: c.paths, JournalDir: c.journal}
}

func (c *managementAccountCoordinator) Recover(ctx context.Context) error {
	c.bind.Lock()
	defer c.bind.Unlock()
	if c.runtime != nil {
		return contract.NewError(contract.CodeInvalidState)
	}
	c.gate.Lock()
	defer c.gate.Unlock()
	if err := c.store.RecoverAccounts(ctx, c.files()); err != nil {
		return err
	}
	accounts, err := aistudio.NewAccountStore(c.paths...).Load()
	if err != nil {
		return err
	}
	seeds := make([]contract.NativeAccount, 0, len(accounts))
	for _, account := range accounts {
		digest, err := aistudio.CredentialDigest(account.StorageState)
		if err != nil {
			return err
		}
		name := account.ID
		state := contract.AccountState{Enabled: account.Config.Enabled, Expired: account.State == aistudio.AccountAuthRequired}
		if !state.Enabled {
			reason := "manual"
			state.DisabledReason = &reason
		}
		if existing, lookupErr := c.store.FindAccountByNativeID(ctx, account.ID); lookupErr == nil {
			// Native files persist the enabled bit, not disabled provenance/times.
			// Preserve those metadata fields on an ordinary restart.
			if account.Config.Enabled == (existing.Enabled && !existing.Archived) {
				state = existing.AccountState
			}
		} else if contract.PublicError(lookupErr).Code != contract.CodeNotFound {
			return lookupErr
		}
		seeds = append(seeds, contract.NativeAccount{NativeID: account.ID, AccountName: &name, State: state, CredentialDigest: digest})
	}
	if err := c.store.ReconcileAccounts(ctx, seeds); err != nil {
		return err
	}
	c.recovered = true
	return nil
}

func (c *managementAccountCoordinator) BindRuntime(ctx context.Context, runtime contract.AccountRuntime) error {
	// Do not enqueue an RWMutex writer while another mutation drains active
	// production leases: their native writes must still enter the read gate.
	for !c.bind.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer c.bind.Unlock()
	if !c.recovered {
		return contract.NewError(contract.CodeInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime == nil {
		c.runtime = nil
		return nil
	}
	if runtime.Epoch() == 0 || c.epochs[runtime.Epoch()] {
		return contract.NewError(contract.CodeInvalidState)
	}
	// Startup recovery already reconciled files. Binding checks the generation's
	// snapshot but does not infer deletion, restore, or enablement from runtime.
	if _, err := runtime.Snapshot(ctx); err != nil {
		return err
	}
	c.runtime = runtime
	c.epochs[runtime.Epoch()] = true
	return nil
}

func (c *managementAccountCoordinator) PrepareCredentials(credentials contract.Credentials) (contract.CredentialMaterial, error) {
	state := aistudio.StorageState{}
	data, err := json.Marshal(credentials)
	if err != nil {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if len(data) > contract.MaxCredentialBytes {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodePayloadTooLarge)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	// Build state explicitly: accountName is public identity input and must not
	// become an opaque native extension in the stored credential JSON.
	state = aistudio.StorageState{Cookies: state.Cookies, Origins: state.Origins}
	id, err := aistudio.NormalizeAccountEmail(credentials.AccountName)
	if err != nil {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if err := state.Validate(); err != nil {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if _, err := aistudio.NewSigner().Sign(state); err != nil {
		return contract.CredentialMaterial{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		return contract.CredentialMaterial{}, err
	}
	return contract.CredentialMaterial{NativeID: id, StorageState: bytes}, nil
}

func expectedAccount(record contract.AccountRecord) contract.ExpectedVersions {
	credential, state := record.CredentialVersion, record.StateVersion
	return contract.ExpectedVersions{Credential: &credential, State: &state}
}

func checkExpected(record contract.AccountRecord, expected contract.ExpectedVersions, required bool) error {
	if err := expected.Validate(required); err != nil {
		return err
	}
	if expected.Credential != nil && (*expected.Credential != record.CredentialVersion || *expected.State != record.StateVersion) {
		return contract.NewError(contract.CodeVersionConflict)
	}
	return nil
}

func (c *managementAccountCoordinator) SnapshotNative(ctx context.Context, nativeID string) (contract.AccountRecord, error) {
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	return c.store.FindAccountByNativeID(ctx, nativeID)
}

func (c *managementAccountCoordinator) SnapshotCredentials(ctx context.Context, id string) (contract.AccountRecord, contract.CredentialMaterial, error) {
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	record, err := c.store.GetAccount(ctx, id)
	if err != nil {
		return record, contract.CredentialMaterial{}, err
	}
	if record.Deleted {
		return record, contract.CredentialMaterial{}, contract.NewError(contract.CodeNotFound)
	}
	directory, err := aistudio.AccountDirectory(c.paths, record.NativeID, false)
	if err != nil {
		return record, contract.CredentialMaterial{}, err
	}
	state, err := aistudio.LoadStorageState(filepath.Join(directory, "storage-state.json"))
	if err != nil {
		return record, contract.CredentialMaterial{}, err
	}
	digest, err := aistudio.CredentialDigest(state)
	if err != nil || digest != record.CredentialDigest {
		return record, contract.CredentialMaterial{}, contract.NewError(contract.CodeVersionConflict)
	}
	bytes, err := json.Marshal(state)
	return record, contract.CredentialMaterial{NativeID: record.NativeID, StorageState: bytes}, err
}

func (c *managementAccountCoordinator) commit(ctx context.Context, intent contract.CommitIntent, files contract.AccountCommitParticipant, force bool) (result contract.AccountCommitResult, resultErr error) {
	if err := intent.Expected.Validate(false); err != nil {
		return result, err
	}
	if intent.OperationID == "" {
		var err error
		intent.OperationID, err = c.newID("op")
		if err != nil {
			return result, err
		}
	}
	c.bind.RLock()
	defer c.bind.RUnlock()
	if !c.recovered || c.runtime == nil {
		return result, contract.NewError(contract.CodeInvalidState)
	}
	leaseCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	var lease contract.AccountMutationLease
	var err error
	if proof, ok := contract.NativeLeaseFromContext(ctx); ok {
		nativeRuntime, supported := c.runtime.(contract.NativeMutationRuntime)
		if !supported || proof.NativeLeaseEpoch() != c.runtime.Epoch() || proof.NativeLeaseAccountID() != intent.NativeID {
			cancel()
			return result, contract.NewError(contract.CodeVersionConflict)
		}
		lease, err = nativeRuntime.AcquireNativeMutation(leaseCtx, proof)
	} else {
		lease, err = c.runtime.AcquireMutation(leaseCtx, intent.NativeID, force)
	}
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return result, contract.NewError(contract.CodeAccountBusy)
		}
		return result, err
	}
	if lease == nil {
		return result, contract.NewError(contract.CodeInvalidState)
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	if lease.Epoch() != c.runtime.Epoch() {
		return result, contract.NewError(contract.CodeVersionConflict)
	}
	c.gate.Lock()
	defer c.gate.Unlock()
	// Read current config only after the shared mutation lease and gate. The
	// file participant merges a proxy override into this same current image.
	if participant, ok := files.(*aistudio.AccountFileParticipant); ok {
		var nextProxy *string
		if participant.NewConfig != nil {
			nextProxy = &participant.NewConfig.Proxy
		}
		if participant.ProxyOverride != nil {
			nextProxy = participant.ProxyOverride
			intent.ProxyApplied = true
		}
		if nextProxy != nil {
			if config.ValidateProxy(*nextProxy) != nil {
				return result, contract.NewError(contract.CodeInvalidRequest)
			}
			if intent.Kind != contract.MutationImport {
				directory, readErr := aistudio.AccountDirectory(c.paths, intent.NativeID, false)
				if readErr != nil {
					return result, readErr
				}
				account, readErr := aistudio.LoadAccountDirectory(directory)
				if readErr != nil {
					return result, readErr
				}
				intent.ProxyChanged = account.Config.Proxy != *nextProxy
			}
		}
	}
	result, err = c.store.CommitAccount(ctx, intent, files)
	// Keep the local publication inside the coordinator gate (never the DB or
	// native storage locks), so a later native cookie commit cannot change the
	// file between this record's commit and its runtime digest verification.
	// A receipt/record returned with a persistence error is still a durable
	// commit. Publication cannot erase it or repeat the credential write.
	if result.Account.AccountID != "" {
		publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		publishErr := lease.Publish(publishCtx, result.Account)
		publishCancel()
		result.Published = publishErr == nil
		if publishErr != nil {
			return result, errors.Join(err, contract.NewError(contract.CodePersistenceError))
		}
	}
	return result, err
}

func (c *managementAccountCoordinator) CommitNative(ctx context.Context, intent contract.CommitIntent, participant contract.AccountCommitParticipant) (contract.AccountCommitResult, error) {
	if participant == nil {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidRequest)
	}
	return c.commit(ctx, intent, participant, false)
}

func (c *managementAccountCoordinator) Import(ctx context.Context, command contract.ImportCommand) (contract.AccountCommitResult, error) {
	if command.Proxy != nil {
		value := strings.TrimSpace(*command.Proxy)
		if config.ValidateProxy(value) != nil {
			return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidRequest)
		}
		command.Proxy = &value
	}
	material, err := c.PrepareCredentials(command.Credentials)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	if _, err := c.SnapshotNative(ctx, material.NativeID); err == nil {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeDuplicateAccount)
	} else if contract.PublicError(err).Code != contract.CodeNotFound {
		return contract.AccountCommitResult{}, err
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		return contract.AccountCommitResult{}, err
	}
	digest, err := aistudio.CredentialDigest(state)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	files := c.files()
	files.NewState = &state
	files.ProxyOverride = command.Proxy
	name := material.NativeID
	return c.commit(ctx, contract.CommitIntent{Kind: contract.MutationImport, NativeID: name, AccountName: &name, CredentialChanged: true, CredentialDigest: digest, Upload: command.Operation.Upload}, files, false)
}

func (c *managementAccountCoordinator) Replace(ctx context.Context, command contract.ReplaceCommand) (contract.AccountCommitResult, error) {
	if command.Proxy != nil {
		value := strings.TrimSpace(*command.Proxy)
		if config.ValidateProxy(value) != nil {
			return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidRequest)
		}
		command.Proxy = &value
	}
	if err := command.Expected.Validate(command.Enable); err != nil {
		return contract.AccountCommitResult{}, err
	}
	record, _, err := c.SnapshotCredentials(ctx, command.AccountID)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	if record.Archived {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidState)
	}
	if err := checkExpected(record, command.Expected, command.Enable); err != nil {
		return contract.AccountCommitResult{}, err
	}
	credentials := command.Credentials
	if credentials.AccountName == "" {
		credentials.AccountName = record.NativeID
	}
	material, err := c.PrepareCredentials(credentials)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	if material.NativeID != record.NativeID {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if command.Candidate != nil {
		if command.Candidate.NativeID != record.NativeID {
			return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidCredentials)
		}
		material = *command.Candidate
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if err := state.Validate(); err != nil {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	if _, err := aistudio.NewSigner().Sign(state); err != nil {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	digest, err := aistudio.CredentialDigest(state)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	files := c.files()
	files.NewState = &state
	files.ProxyOverride = command.Proxy
	intent := contract.CommitIntent{Kind: contract.MutationReplace, AccountID: record.AccountID, NativeID: record.NativeID, Expected: expectedAccount(record), CredentialChanged: true, CredentialDigest: digest, Upload: command.Operation.Upload}
	if command.Enable {
		next := contract.AccountState{Enabled: true}
		intent.State = &next
	}
	return c.commit(ctx, intent, files, false)
}

func (c *managementAccountCoordinator) state(ctx context.Context, command contract.StateCommand, kind contract.MutationKind) (contract.AccountCommitResult, error) {
	record, err := c.store.GetAccount(ctx, command.AccountID)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	if record.Deleted {
		return contract.AccountCommitResult{}, contract.NewError(contract.CodeNotFound)
	}
	if err := checkExpected(record, command.Expected, false); err != nil {
		return contract.AccountCommitResult{}, err
	}
	next := record.AccountState
	switch kind {
	case contract.MutationState:
		if record.Archived {
			return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidState)
		}
		next.Enabled = command.Enabled
		if next.Enabled == record.Enabled {
			break
		}
		if next.Enabled {
			next.DisabledReason = nil
			next.DisabledStatus = nil
			next.DisabledAt = nil
		} else {
			reason := "manual"
			at := contract.NewTimestamp(c.clock())
			next.DisabledReason = &reason
			next.DisabledAt = &at
			next.DisabledStatus = nil
		}
	case contract.MutationArchive:
		if record.Archived {
			break
		}
		next.Enabled = false
		next.Archived = true
		reason := "archived"
		at := contract.NewTimestamp(c.clock())
		next.DisabledReason = &reason
		next.DisabledStatus = nil
		next.DisabledAt = &at
	case contract.MutationRestore:
		if !record.Archived {
			return contract.AccountCommitResult{}, contract.NewError(contract.CodeInvalidState)
		}
		next.Enabled = false
		next.Archived = false
		reason := "manual"
		at := contract.NewTimestamp(c.clock())
		next.DisabledReason = &reason
		next.DisabledStatus = nil
		next.DisabledAt = &at
	case contract.MutationDelete:
		next.Enabled = false
	}
	return c.commit(ctx, contract.CommitIntent{Kind: kind, AccountID: record.AccountID, NativeID: record.NativeID, Expected: expectedAccount(record), State: &next, CredentialDigest: record.CredentialDigest}, c.files(), command.Force)
}

func (c *managementAccountCoordinator) Patch(ctx context.Context, command contract.StateCommand) (contract.AccountCommitResult, error) {
	return c.state(ctx, command, contract.MutationState)
}
func (c *managementAccountCoordinator) Archive(ctx context.Context, command contract.StateCommand) (contract.AccountCommitResult, error) {
	return c.state(ctx, command, contract.MutationArchive)
}
func (c *managementAccountCoordinator) Restore(ctx context.Context, command contract.StateCommand) (contract.AccountCommitResult, error) {
	return c.state(ctx, command, contract.MutationRestore)
}

func (c *managementAccountCoordinator) Reload(ctx context.Context, command contract.StateCommand) (contract.AccountCommitResult, error) {
	record, _, err := c.SnapshotCredentials(ctx, command.AccountID)
	if err != nil {
		return contract.AccountCommitResult{}, err
	}
	if err := checkExpected(record, command.Expected, false); err != nil {
		return contract.AccountCommitResult{}, err
	}
	return c.commit(ctx, contract.CommitIntent{Kind: contract.MutationState, AccountID: record.AccountID, NativeID: record.NativeID, Expected: expectedAccount(record), CredentialDigest: record.CredentialDigest}, c.files(), command.Force)
}

func (c *managementAccountCoordinator) Export(ctx context.Context, ids []string) (contract.AccountExport, error) {
	result := contract.AccountExport{Items: make([]contract.ExportItem, 0, len(ids))}
	for _, id := range ids {
		record, material, err := c.SnapshotCredentials(ctx, id)
		if err != nil {
			return result, err
		}
		var credentials contract.Credentials
		if err := json.Unmarshal(material.StorageState, &credentials); err != nil {
			return result, err
		}
		credentials.AccountName = record.NativeID
		result.Items = append(result.Items, contract.ExportItem{AccountID: id, Index: record.Index, Credentials: credentials})
	}
	return result, nil
}

var _ contract.AccountCoordinator = (*managementAccountCoordinator)(nil)

func prepareManagementFingerprint(source string) ([]byte, error) {
	if source == "" {
		return nil, nil
	}
	temporary, err := os.MkdirTemp("", "aistudio2api-fingerprint-prepare-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	if err := camoufoxnative.PersistAccountFingerprint(source, temporary); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(temporary, "camoufox-fingerprint.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
