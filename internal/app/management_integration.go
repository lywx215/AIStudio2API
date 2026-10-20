package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/config"
	"github.com/Mag1cFall/AIStudio2API/internal/management"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementmaintenance"
	"github.com/Mag1cFall/AIStudio2API/internal/managementstore"
	"github.com/Mag1cFall/AIStudio2API/internal/managementverify"
)

type managementConstruction struct {
	services *contract.ProcessServices
	journal  string
	epoch    uint64
}
type managementConstructionKey struct{}

var managementRuntimeEpoch atomic.Uint64

// Called only after process task recovery/start, never by StartService or a
// generation binding. The loop's package has no store/task implementation deps.
func (manager *runtimeManager) startManagementMaintenance(ctx context.Context) error {
	if manager.closed.Load() || manager.management == nil || manager.management.Store == nil {
		return contract.NewError(contract.CodeInvalidState)
	}
	if manager.managementMaintenance != nil {
		return nil
	}
	service, err := managementmaintenance.Start(ctx, manager.management.Store, managementmaintenance.Options{OnFailure: func(code contract.ErrorCode) {
		if manager.requests != nil {
			manager.requests.log("management", "WARN", "Management retention maintenance failed | code="+string(code))
		}
	}})
	if err != nil {
		return err
	}
	manager.managementMaintenance = service
	return nil
}

// prepareManagement recovers the real file/store participants before any account
// pool is constructed. Constructors below do not launch model/browser actions.
func (manager *runtimeManager) prepareManagement(ctx context.Context, cfg config.Config) error {
	root := filepath.Join(filepath.Dir(manager.configPath), "runtime", "management")
	store, err := managementstore.Open(ctx, contract.StoreOptions{Path: filepath.Join(root, "management.db")})
	if err != nil {
		return err
	}
	manager.management = &contract.ProcessServices{Store: store}
	manager.managementJournal = filepath.Join(root, "account-journals")
	coordinator, err := newManagementAccountCoordinator(contract.AccountCoordinatorOptions{Store: store, AuthPaths: strings.Split(cfg.AuthStates, ","), JournalDir: manager.managementJournal})
	if err != nil {
		return err
	}
	manager.management.Accounts = coordinator
	if err := coordinator.Recover(ctx); err != nil {
		return err
	}
	target, err := newManagementVerificationTarget(managementVerificationTargetOptions{Accounts: coordinator, Environment: manager.managementVerificationEnvironment})
	if err != nil {
		return err
	}
	verifier, err := managementverify.New(contract.VerifierOptions{Target: target, Concurrency: 1})
	if err != nil {
		return err
	}
	manager.management.Verifier = verifier
	tasks, err := management.NewTaskService(contract.TaskServiceOptions{Store: store, Accounts: coordinator, Verifier: verifier, QueueLimit: 100})
	if err != nil {
		return err
	}
	manager.management.Tasks = tasks
	httpRegistrar, err := management.NewHTTP(contract.HTTPOptions{Store: store, Tasks: tasks, Accounts: coordinator, Projection: &managementProjection{manager: manager}})
	if err != nil {
		return err
	}
	manager.management.HTTP = httpRegistrar
	return nil
}

func (manager *runtimeManager) buildGeneration(ctx context.Context, cfg config.Config) (*runtimeGeneration, error) {
	if manager.management == nil {
		return manager.factory(ctx, manager.lifecycle, cfg, manager.requests)
	}
	if cfg.AuthStates != manager.activeManagement.AuthStates {
		// Process coordinator paths cannot be silently rebound while old account
		// operations and identity mappings are alive. Reconfigure on process restart.
		return nil, contract.NewError(contract.CodeInvalidState)
	}
	epoch := managementRuntimeEpoch.Add(1)
	if epoch == 0 {
		return nil, contract.NewError(contract.CodeInvalidState)
	}
	ctx = context.WithValue(ctx, managementConstructionKey{}, managementConstruction{services: manager.management, journal: manager.managementJournal, epoch: epoch})
	generation, err := manager.factory(ctx, manager.lifecycle, cfg, manager.requests)
	if err != nil {
		return nil, err
	}
	admin, ok := generation.admin.(*runtimeAdmin)
	if !ok {
		return nil, errors.Join(contract.NewError(contract.CodeInvalidState), generation.Close())
	}
	generation.managementRuntime = &managementAccountRuntime{epoch: epoch, admin: admin, store: manager.management.Store}
	return generation, nil
}

func (manager *runtimeManager) bindManagement(ctx context.Context, generation *runtimeGeneration) error {
	if manager.management == nil {
		return nil
	}
	if generation.managementRuntime == nil {
		return contract.NewError(contract.CodeInvalidState)
	}
	return manager.management.Accounts.BindRuntime(ctx, generation.managementRuntime)
}

// The verifier receives copied account-specific configuration only. It never
// gets a production worker, pool, credential path or caller-selected fallback.
func (manager *runtimeManager) managementVerificationEnvironment(ctx context.Context, nativeID string) (managementVerificationEnvironment, error) {
	if err := ctx.Err(); err != nil {
		return managementVerificationEnvironment{}, err
	}
	g, cfg := manager.generationConfigSnapshot()
	if g == nil {
		return managementVerificationEnvironment{}, contract.NewError(contract.CodeInvalidState)
	}
	admin, ok := g.admin.(*runtimeAdmin)
	if !ok {
		return managementVerificationEnvironment{}, contract.NewError(contract.CodeInvalidState)
	}
	admin.workers.mu.RLock()
	worker := admin.workers.accounts[nativeID]
	admin.workers.mu.RUnlock()
	if worker == nil {
		return managementVerificationEnvironment{}, contract.NewError(contract.CodeNotFound)
	}
	worker.mu.Lock()
	options := worker.config
	worker.mu.Unlock()
	initTimeout := cfg.InitTimeout
	if initTimeout <= 0 {
		initTimeout = 2 * time.Minute
	}
	return managementVerificationEnvironment{WAABackend: cfg.WAABackend, CamoufoxPath: admin.workers.camoufox, Proxy: options.Proxy, Locale: options.Locale, Timezone: options.Timezone, InitTimeout: initTimeout}, nil
}
