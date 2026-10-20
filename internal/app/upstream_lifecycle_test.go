package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

type accountDelegateAdmin struct {
	api.AdminService
	assertUnlocked func()
	view           *camoufoxnative.LiveView
}

func (admin *accountDelegateAdmin) CreateAccount(_ context.Context, _ api.AccountCreateInput, view *camoufoxnative.LiveView) (api.AdminAccount, error) {
	admin.assertUnlocked()
	admin.view = view
	return api.AdminAccount{ID: "created"}, nil
}
func (admin *accountDelegateAdmin) LoginAccount(_ context.Context, id string, view *camoufoxnative.LiveView) (api.AdminAccount, error) {
	admin.assertUnlocked()
	admin.view = view
	return api.AdminAccount{ID: id}, nil
}
func (admin *accountDelegateAdmin) CookieBrowsers(context.Context) ([]api.CookieBrowser, error) {
	admin.assertUnlocked()
	return []api.CookieBrowser{{Address: "127.0.0.1:1"}}, nil
}

func TestRuntimeManagerAccountDelegatesReleaseManagerLock(t *testing.T) {
	manager := &runtimeManager{}
	admin := &accountDelegateAdmin{assertUnlocked: func() {
		if !manager.mu.TryLock() {
			t.Error("manager lock held across account delegate")
			return
		}
		manager.mu.Unlock()
	}}
	manager.current = &runtimeGeneration{admin: admin}
	view := &camoufoxnative.LiveView{}
	created, err := manager.CreateAccount(context.Background(), api.AccountCreateInput{}, view)
	if err != nil || created.ID != "created" || admin.view != view {
		t.Fatal("CreateAccount lost its login view or result")
	}
	loggedIn, err := manager.LoginAccount(context.Background(), "synthetic@example.invalid", view)
	if err != nil || loggedIn.ID != "synthetic@example.invalid" || admin.view != view {
		t.Fatal("LoginAccount lost its login view or result")
	}
	browsers, err := manager.CookieBrowsers(context.Background())
	if err != nil || len(browsers) != 1 {
		t.Fatal("CookieBrowsers lost its result")
	}
}

func TestRuntimeConfigPreservesMediaAndModelChannels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "synthetic.env")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	pool := aistudio.NewAccountPool(nil, 1)
	admin := &runtimeAdmin{configPath: path, requests: newRequestRegistry(ctx), pool: pool, service: &trackedService{pool: pool}}
	value := runtimeConfigDTO(cfg)
	value.ModelChannels = "synthetic-model:build"
	value.MediaOutput = config.MediaOutputURL
	value.MediaBaseURL = "https://media.example.invalid/gateway"
	updated, err := admin.UpdateRuntimeConfig(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []api.RuntimeConfig{updated, runtimeConfigDTO(saved)} {
		if got.ModelChannels != value.ModelChannels || got.MediaOutput != value.MediaOutput || got.MediaBaseURL != value.MediaBaseURL {
			t.Fatal("runtime configuration lost media or channel fields")
		}
	}
	manager := &runtimeManager{activeManagement: cfg}
	if got := manager.decorateRuntimeConfig(updated, cfg); !got.ServiceRestartRequired || got.ManagementRestartRequired {
		t.Fatal("media changes must restart generation only")
	}
	if got := manager.decorateRuntimeConfig(updated, saved); got.ServiceRestartRequired {
		t.Fatal("new generation still reports a media restart")
	}
	manager.current = &runtimeGeneration{config: saved}
	if enabled, base := manager.mediaLinks(); !enabled || base != value.MediaBaseURL {
		t.Fatal("media links lost the active generation configuration")
	}
	// Older console clients omit the new field. Saving their full DTO keeps
	// the pre-upgrade inline behavior instead of rejecting the request.
	updated.MediaOutput = ""
	updated.MediaBaseURL = ""
	updated, err = admin.UpdateRuntimeConfig(ctx, updated)
	if err != nil || updated.MediaOutput != config.MediaOutputInline || updated.ModelChannels != value.ModelChannels {
		t.Fatalf("legacy runtime DTO default: mode=%q err=%v", updated.MediaOutput, err)
	}
}

type configDelegateAdmin struct{ api.AdminService }

func (configDelegateAdmin) UpdateRuntimeConfig(_ context.Context, value api.RuntimeConfig) (api.RuntimeConfig, error) {
	return value, nil
}

type configDelegateService struct {
	aistudio.Service
	capacityCalls int
}

func (*configDelegateService) State() string { return "STOPPED" }
func (service *configDelegateService) ApplyCapacity(int, int, int, int) {
	service.capacityCalls++
}

func TestRuntimeManagerHotChannelsKeepMediaPendingRestart(t *testing.T) {
	cfg := config.Default()
	service := &configDelegateService{}
	manager := &runtimeManager{activeManagement: cfg, current: &runtimeGeneration{
		config: cfg, admin: configDelegateAdmin{}, service: service,
	}}
	value := runtimeConfigDTO(cfg)
	value.ModelChannels = "synthetic-model:build"
	value.MediaOutput = config.MediaOutputURL
	value.MediaBaseURL = "https://media.example.invalid/gateway"
	updated, err := manager.UpdateRuntimeConfig(context.Background(), value)
	if err != nil || service.capacityCalls != 1 {
		t.Fatalf("runtime update: capacity=%d err=%v", service.capacityCalls, err)
	}
	if manager.current.config.ModelChannels != value.ModelChannels {
		t.Fatal("active generation lost immediately applied model channels")
	}
	if enabled, _ := manager.mediaLinks(); enabled || !updated.ServiceRestartRequired || updated.ManagementRestartRequired {
		t.Fatal("pending media configuration bypassed generation restart")
	}
}
