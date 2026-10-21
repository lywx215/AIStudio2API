package app

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	p "github.com/Mag1cFall/AIStudio2API/internal/proxymanager"
)

type proxyAccountBackend struct {
	coordinator *managementAccountCoordinator
	manager     *runtimeManager
}

func proxyError(err error) error {
	if err == nil {
		return nil
	}
	code := string(c.PublicError(err).Code)
	return &p.Error{Code: code}
}
func (b *proxyAccountBackend) Snapshot(ctx context.Context, id string) (p.Account, error) {
	coordinator := b.coordinator
	coordinator.bind.RLock()
	defer coordinator.bind.RUnlock()
	coordinator.gate.Lock()
	defer coordinator.gate.Unlock()
	record, err := coordinator.store.FindAccountByNativeID(ctx, id)
	if err != nil {
		return p.Account{}, proxyError(err)
	}
	a := p.Account{ID: record.NativeID, UUID: record.AccountID, CredentialVersion: record.CredentialVersion, StateVersion: record.StateVersion, Archived: record.Archived, Deleted: record.Deleted, Enabled: record.Enabled}
	if record.Deleted {
		return a, nil
	}
	directory, err := aistudio.AccountDirectory(coordinator.paths, record.NativeID, false)
	if err != nil {
		return a, proxyError(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "account.json"))
	if err != nil {
		return a, &p.Error{Code: "ACCOUNT_CONFIG_READ_FAILED"}
	}
	var config aistudio.AccountConfig
	if json.Unmarshal(data, &config) != nil {
		return a, &p.Error{Code: "ACCOUNT_CONFIG_READ_FAILED"}
	}
	a.URL = config.Proxy
	if b.manager != nil {
		if generation := b.manager.generationSnapshot(); generation != nil {
			if admin, ok := generation.admin.(*runtimeAdmin); ok {
				if account, err := admin.pool.Account(id); err == nil {
					a.URL = account.EffectiveProxy(admin.globalProxy)
				}
			}
		}
	}
	if u, err := url.Parse(a.URL); err == nil {
		a.Endpoint = u.Host
	}
	return a, nil
}
func (b *proxyAccountBackend) Apply(ctx context.Context, work p.Work) (p.Receipt, error) {
	record, err := b.coordinator.store.GetAccount(ctx, work.Account.UUID)
	if err != nil {
		return p.Receipt{}, proxyError(err)
	}
	if record.NativeID != work.Account.ID || record.Archived || record.Deleted {
		return p.Receipt{}, &p.Error{Code: "ACCOUNT_ARCHIVED_OR_DELETED"}
	}
	credential, state := work.Account.CredentialVersion, work.Account.StateVersion
	files := b.coordinator.files()
	files.ProxyOverride = &work.URL
	result, err := b.coordinator.commit(ctx, c.CommitIntent{OperationID: work.OperationID, Kind: c.MutationState, AccountID: record.AccountID, NativeID: record.NativeID, Expected: c.ExpectedVersions{Credential: &credential, State: &state}, CredentialDigest: record.CredentialDigest}, files, false)
	return p.Receipt{AccountID: result.Account.AccountID, CredentialVersion: result.Account.CredentialVersion, StateVersion: result.Account.StateVersion}, proxyError(err)
}
func (b *proxyAccountBackend) Committed(ctx context.Context, operationID string) (p.Receipt, bool, error) {
	reader, ok := b.coordinator.store.(interface {
		ReadProxyCommit(context.Context, string) (c.AccountCommitResult, bool, error)
	})
	if !ok {
		return p.Receipt{}, false, &p.Error{Code: "COMMIT_READBACK_UNAVAILABLE"}
	}
	result, found, err := reader.ReadProxyCommit(ctx, operationID)
	return p.Receipt{AccountID: result.Account.AccountID, CredentialVersion: result.Account.CredentialVersion, StateVersion: result.Account.StateVersion}, found, proxyError(err)
}
func (b *proxyAccountBackend) Counts(context.Context) (map[string]int, error) {
	counts := map[string]int{}
	if b.manager == nil {
		return counts, nil
	}
	generation := b.manager.generationSnapshot()
	if generation == nil {
		return counts, nil
	}
	admin, ok := generation.admin.(*runtimeAdmin)
	if !ok {
		return counts, nil
	}
	for _, status := range admin.pool.Status() {
		if status.Proxy != "" {
			counts[status.Proxy]++
		}
	}
	return counts, nil
}
func (manager *runtimeManager) prepareProxyManagement(ctx context.Context) error {
	if manager.proxyManagement != nil {
		return nil
	}
	coordinator, ok := manager.management.Accounts.(*managementAccountCoordinator)
	if !ok {
		return &p.Error{Code: "ACCOUNT_SERVICE_UNAVAILABLE"}
	}
	service, err := p.Open(ctx, p.Options{Path: filepath.Join(filepath.Dir(manager.configPath), "runtime", "proxies", "proxies.db"), Accounts: &proxyAccountBackend{coordinator: coordinator, manager: manager}})
	if err != nil {
		return err
	}
	manager.proxyManagement = service
	return nil
}
