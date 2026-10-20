package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// managementProjection is an explicit allowlist adapter. It never serializes
// native AdminAccount, RuntimeConfig, logs, request bodies or proxy settings.
type managementProjection struct{ manager *runtimeManager }

func (manager *runtimeManager) generationSnapshot() *runtimeGeneration {
	manager.mu.RLock()
	generation := manager.current
	manager.mu.RUnlock()
	return generation
}

func (manager *runtimeManager) generationConfigSnapshot() (*runtimeGeneration, config.Config) {
	manager.mu.RLock()
	generation := manager.current
	var cfg config.Config
	if generation != nil {
		cfg = generation.config
		cfg.UpstreamChannels = slices.Clone(cfg.UpstreamChannels)
	}
	manager.mu.RUnlock()
	return generation, cfg
}

func (p *managementProjection) Status(ctx context.Context) (contract.SystemStatus, error) {
	if err := ctx.Err(); err != nil {
		return contract.SystemStatus{}, err
	}
	g := p.manager.generationSnapshot()
	if g == nil || g.service == nil {
		backend := p.manager.activeManagement.WAABackend
		if backend == "" {
			backend = config.WAABackendCamoufox
		}
		return contract.SystemStatus{ServiceState: "STOPPED", WAABackend: backend}, nil
	}
	result := contract.SystemStatus{ServiceState: g.service.State(), WAABackend: g.config.WAABackend}
	if service, ok := g.service.(*trackedService); ok && service.pool != nil {
		statuses := service.pool.Status()
		result.AccountCount = len(statuses)
		for _, account := range statuses {
			if account.Enabled {
				result.EnabledAccountCount++
			}
			if account.State == aistudio.AccountReady || account.State == aistudio.AccountBusy {
				result.Ready = true
			}
		}
		if service.workers != nil {
			result.ActiveContextsCount = service.workers.occupiedWorkers().slots
			result.BrowserConnected = g.config.WAABackend == config.WAABackendCamoufox && len(service.workers.ReadyWarmAccountIDs()) > 0
		}
	} else {
		// This path supports other real managedService implementations and test
		// adapters; only the typed safe status counters cross this boundary.
		if g.admin == nil {
			return contract.SystemStatus{}, contract.NewError(contract.CodeInvalidState)
		}
		status, err := g.admin.Status(ctx)
		if err != nil {
			return contract.SystemStatus{}, err
		}
		result.Ready = status.Ready
		result.AccountCount = status.Accounts.Total
	}
	result.Ready = result.Ready && result.ServiceState == "RUNNING"
	result.IsSystemBusy = result.ServiceState == "LAUNCHING" || result.ServiceState == "STOPPING"
	return result, nil
}

func (p *managementProjection) Readiness(ctx context.Context) (contract.Readiness, error) {
	status, err := p.Status(ctx)
	if err != nil {
		return contract.Readiness{}, err
	}
	return contract.Readiness{Ready: status.Ready, Checks: []contract.Check{
		{Name: "generation_running", Ready: status.ServiceState == "RUNNING"},
		{Name: "eligible_account", Ready: status.Ready},
	}}, nil
}

func (p *managementProjection) Capabilities(ctx context.Context) (contract.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return contract.Capabilities{}, err
	}
	services := p.manager.management
	active := services != nil && services.Store != nil && services.Accounts != nil && services.Tasks != nil
	verification := active && services.Verifier != nil
	channels := []contract.Channel{}
	if verification {
		channels = append(channels, contract.ChannelPlayground)
	}
	return contract.Capabilities{Backend: "aistudio2api", ProtocolVersion: contract.ProtocolVersion, StableAccountIDs: active, DualCAS: active, UploadReceipts: active, TargetedVerification: verification, VerificationChannels: channels, SettingsWrite: false}, nil
}

func (p *managementProjection) Settings(ctx context.Context) (contract.SettingsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.SettingsSnapshot{}, err
	}
	g, cfg := p.manager.generationConfigSnapshot()
	if g == nil {
		return contract.SettingsSnapshot{}, contract.NewError(contract.CodeInvalidState)
	}
	return contract.SettingsSnapshot{Values: contract.SettingsValues{
		WAABackend: cfg.WAABackend, WarmWorkerLimit: cfg.WarmWorkerLimit, MaxActiveWorkers: cfg.MaxActiveWorkers,
		WarmStartupConcurrency: cfg.WarmStartupConcurrency, PerAccountConcurrency: cfg.PerAccountConcurrency,
		RoutingStrategy: cfg.RoutingStrategy, UpstreamChannels: slices.Clone(cfg.UpstreamChannels),
	}, PersistentKeys: []string{"waaBackend", "warmWorkerLimit", "maxActiveWorkers", "warmStartupConcurrency", "perAccountConcurrency", "routingStrategy", "upstreamChannels"}}, nil
}

func (*managementProjection) PatchSettings(context.Context, contract.SettingsPatch) (contract.SettingsUpdated, error) {
	return contract.SettingsUpdated{}, contract.NewError(contract.CodeInvalidState)
}

func (p *managementProjection) Diagnostics(ctx context.Context, query contract.PageQuery) (contract.Diagnostics, error) {
	if err := query.Validate(); err != nil {
		return contract.Diagnostics{}, err
	}
	services := p.manager.management
	if services == nil || services.Store == nil {
		return contract.Diagnostics{}, contract.NewError(contract.CodeInvalidState)
	}
	page, err := services.Store.ListAccounts(ctx, query)
	if err != nil {
		return contract.Diagnostics{}, err
	}
	g := p.manager.generationSnapshot()
	if g == nil {
		return contract.Diagnostics{}, contract.NewError(contract.CodeInvalidState)
	}
	result := contract.Diagnostics{ServiceState: g.service.State(), WAABackend: g.config.WAABackend, Accounts: contract.Page[contract.AccountRuntimeState]{Items: []contract.AccountRuntimeState{}, Total: page.Total, Offset: page.Offset, Limit: page.Limit}}
	states := map[string]aistudio.AccountStatus{}
	if service, ok := g.service.(*trackedService); ok && service.pool != nil {
		for _, account := range service.pool.Status() {
			states[account.ID] = account
		}
	}
	active := map[string]int{}
	if p.manager.requests != nil {
		for _, request := range p.manager.requests.list() {
			active[request.AccountID]++
		}
	}
	for _, account := range page.Items {
		record, err := services.Store.GetAccount(ctx, account.AccountID)
		if err != nil {
			return contract.Diagnostics{}, err
		}
		item := contract.AccountRuntimeState{AccountID: account.AccountID, State: "unavailable", ActiveRequests: active[record.NativeID]}
		if account.Archived {
			item.State = "archived"
		} else if !account.Enabled {
			item.State = "disabled"
		} else if native, ok := states[record.NativeID]; ok {
			item.State = string(native.State)
			var until time.Time
			for _, cooldown := range native.Cooldowns {
				if cooldown.Until.After(until) {
					until = cooldown.Until
				}
			}
			if !until.IsZero() {
				at := contract.NewTimestamp(until)
				item.CooldownUntil = &at
			}
		}
		result.Accounts.Items = append(result.Accounts.Items, item)
	}
	return result, nil
}

func (p *managementProjection) Usage(ctx context.Context, query contract.PageQuery) (contract.Page[contract.UsageRecord], error) {
	if err := query.Validate(); err != nil {
		return contract.Page[contract.UsageRecord]{}, err
	}
	if p.manager.ledger == nil {
		return contract.Page[contract.UsageRecord]{}, contract.NewError(contract.CodeInvalidState)
	}
	page, err := p.manager.ledger.ManagementUsage(ctx, query)
	if err != nil {
		return contract.Page[contract.UsageRecord]{}, errors.Join(contract.NewError(contract.CodePersistenceError), err)
	}
	return page, nil
}

var _ contract.Projection = (*managementProjection)(nil)
