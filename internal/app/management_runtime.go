package app

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// managementAccountRuntime is pinned to one generation. The process coordinator
// owns the BindRuntime barrier; this adapter never takes runtimeManager.mu.
type managementAccountRuntime struct {
	epoch       uint64
	admin       *runtimeAdmin
	store       contract.AccountMetadataStore
	gates       sync.Map // normalized native email -> chan struct{}
	nativeGates sync.Map // native commit -> publication, without production drain
}

func (r *managementAccountRuntime) Epoch() uint64 { return r.epoch }

func (r *managementAccountRuntime) Snapshot(ctx context.Context) ([]contract.NativeAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accounts, err := r.admin.store.Load()
	if err != nil {
		return nil, err
	}
	items := make([]contract.NativeAccount, 0, len(accounts))
	if r.store != nil {
		// BindRuntime calls Snapshot under its exclusive barrier. Re-read and
		// synchronize this dormant generation after construction: native/task
		// writes may have completed on the old generation while it was built.
		if r.admin.service.State() != "STOPPED" {
			return nil, contract.NewError(contract.CodeInvalidState)
		}
		ids := map[string]bool{}
		for _, account := range accounts {
			ids[account.ID] = true
		}
		for _, account := range r.admin.pool.Status() {
			ids[account.ID] = true
		}
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		for _, id := range ordered {
			record, err := r.store.FindAccountByNativeID(ctx, id)
			if err != nil {
				if contract.PublicError(err).Code != contract.CodeNotFound {
					return nil, err
				}
				record = contract.AccountRecord{NativeID: id, Deleted: true}
			}
			if record.Archived {
				record.Deleted = true
			} // local exclusion only, no metadata/file mutation
			if _, err := r.admin.pool.Account(id); errors.Is(err, aistudio.ErrAccountNotFound) && record.Deleted {
				continue
			}
			lease, err := r.AcquireMutation(ctx, id, false)
			if err != nil {
				return nil, err
			}
			err = lease.Publish(ctx, record)
			if err = errors.Join(err, lease.Release()); err != nil {
				return nil, err
			}
		}
	}
	for _, account := range accounts {
		digest, err := aistudio.CredentialDigest(account.StorageState)
		if err != nil {
			return nil, err
		}
		name := account.ID
		items = append(items, contract.NativeAccount{NativeID: name, AccountName: &name, State: contract.AccountState{Enabled: account.Config.Enabled, Expired: account.State == aistudio.AccountAuthRequired}, CredentialDigest: digest})
	}
	return items, nil
}

func (r *managementAccountRuntime) AcquireNativeMutation(ctx context.Context, proof contract.NativeLeaseProof) (contract.AccountMutationLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if proof == nil || !proof.NativeLeaseActive() || proof.NativeLeaseEpoch() != r.epoch || proof.NativeLeaseAccountID() == "" {
		return nil, contract.NewError(contract.CodeVersionConflict)
	}
	value, _ := r.nativeGates.LoadOrStore(proof.NativeLeaseAccountID(), make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if !proof.NativeLeaseActive() || proof.NativeLeaseEpoch() != r.epoch {
		<-gate
		return nil, contract.NewError(contract.CodeVersionConflict)
	}
	// The native production lease continues to own its admission/file lifetime.
	// Taking the management drain gate here would deadlock its waiting writer.
	return &managementMutationLease{runtime: r, nativeID: proof.NativeLeaseAccountID(), native: true, gate: gate}, nil
}

func (r *managementAccountRuntime) AcquireMutation(ctx context.Context, nativeID string, force bool) (contract.AccountMutationLease, error) {
	if proof, ok := contract.NativeLeaseFromContext(ctx); ok {
		if proof.NativeLeaseAccountID() != nativeID {
			return nil, contract.NewError(contract.CodeVersionConflict)
		}
		return r.AcquireNativeMutation(ctx, proof)
	}
	if existing, ok := aistudio.AccountLeaseFromContext(ctx); ok {
		if !existing.BelongsToPool(r.admin.pool) || existing.Account().ID != nativeID {
			return nil, contract.NewError(contract.CodeVersionConflict)
		}
		// Reuse the established native lease context with the same active/epoch
		// checks; never acquire its own exclusive drain a second time.
		return r.AcquireNativeMutation(ctx, existing)
	}
	gateValue, _ := r.gates.LoadOrStore(nativeID, make(chan struct{}, 1))
	gate := gateValue.(chan struct{})
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	lease := &managementMutationLease{runtime: r, nativeID: nativeID, gate: gate}
	if force {
		r.admin.requests.mu.Lock()
		var cancels []context.CancelFunc
		for _, request := range r.admin.requests.active {
			if request.request.AccountID == nativeID {
				cancels = append(cancels, request.cancel)
			}
		}
		r.admin.requests.mu.Unlock()
		for _, cancel := range cancels {
			if cancel != nil {
				cancel()
			}
		}
	}
	accountLease, err := r.admin.pool.AcquireManagementAccount(ctx, nativeID)
	if errors.Is(err, aistudio.ErrAccountNotFound) {
		return lease, nil
	} // unpublished create/recovery
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	lease.account = accountLease
	// The exclusive production lease stays held from drain through publication;
	// stopping the worker now also stops its fingerprint-file writer.
	if err := r.admin.workers.Reset(nativeID); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return lease, nil
}

type managementMutationLease struct {
	runtime   *managementAccountRuntime
	nativeID  string
	native    bool
	account   *aistudio.AccountLease
	gate      chan struct{}
	mu        sync.Mutex
	released  bool
	published bool
}

func (l *managementMutationLease) Epoch() uint64 { return l.runtime.epoch }

func (l *managementMutationLease) Publish(ctx context.Context, record contract.AccountRecord) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || record.NativeID != l.nativeID {
		return contract.NewError(contract.CodeVersionConflict)
	}
	if l.published {
		return nil
	}
	admin := l.runtime.admin
	stale := false
	defer func() {
		if err != nil && !stale {
			_ = admin.pool.MarkUnavailable(record.NativeID, "Management publication pending.")
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.runtime.store != nil && !record.Deleted {
		latest, lookupErr := l.runtime.store.GetAccount(ctx, record.AccountID)
		if lookupErr != nil {
			if contract.PublicError(lookupErr).Code == contract.CodeNotFound {
				stale = true
				return contract.NewError(contract.CodeVersionConflict)
			}
			return lookupErr
		}
		if latest.Versions != record.Versions || latest.CredentialDigest != record.CredentialDigest {
			stale = true
			return contract.NewError(contract.CodeVersionConflict)
		}
	}
	if l.native {
		// In-flight Cookie/auth writes preserve their worker and lineage. Native
		// refresh's own header preparation callback handles its cached headers.
		_, err = admin.pool.PublishManagementAccount(record, true)
		l.published = err == nil
		return err
	}
	_, lookupErr := admin.pool.Account(record.NativeID)
	if errors.Is(lookupErr, aistudio.ErrAccountNotFound) && !record.Deleted {
		directory, err := aistudio.AccountDirectory(admin.store.AuthPaths(), record.NativeID, false)
		if err != nil {
			return err
		}
		account, err := aistudio.LoadAccountDirectory(filepath.Clean(directory))
		if err != nil {
			return err
		}
		account.Config.Enabled = record.Enabled && !record.Archived
		if err := admin.headers.Add(account); err != nil {
			return err
		}
		if err := admin.workers.Add(account); err != nil {
			_ = admin.headers.Remove(account.ID)
			return err
		}
		if err := admin.service.changeModels(func() error { return admin.pool.Add(account) }); err != nil {
			_ = admin.workers.Remove(account.ID)
			_ = admin.headers.Remove(account.ID)
			return err
		}
		l.published = true
		return nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, aistudio.ErrAccountNotFound) {
		return lookupErr
	}
	if record.Deleted {
		if lookupErr == nil {
			if err := admin.workers.Remove(record.NativeID); err != nil {
				return err
			}
			if err := admin.headers.Remove(record.NativeID); err != nil {
				return err
			}
		}
		err = admin.service.changeModels(func() error { _, err := admin.pool.PublishManagementAccount(record, false); return err })
		l.published = err == nil
		return err
	}
	var account *aistudio.Account
	err = admin.service.changeModels(func() error {
		var err error
		account, err = admin.pool.PublishManagementAccount(record, false)
		return err
	})
	if err != nil {
		return err
	}
	headerUpdate, err := admin.headers.prepareUpdate(account, account.Config)
	if err != nil {
		return err
	}
	workerUpdate, err := admin.workers.prepareUpdate(account, account.Config)
	if err != nil {
		headerUpdate.Discard()
		return err
	}
	workerUpdate.Commit()
	headerUpdate.Commit()
	l.published = true
	return nil
}

func (l *managementMutationLease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	var err error
	if l.account != nil {
		err = l.account.Release()
	}
	if l.gate != nil {
		<-l.gate
	}
	return err
}

var _ contract.AccountRuntime = (*managementAccountRuntime)(nil)
var _ contract.NativeMutationRuntime = (*managementAccountRuntime)(nil)
