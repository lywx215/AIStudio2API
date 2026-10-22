package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/host"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

// newBusinessAddon composes the add-on outside runtimeGeneration. AI Studio's
// start, stop, WAA preparation, account pool and protocol handlers are unchanged.
func newBusinessAddon(manager *runtimeManager) (*api.BusinessAddon, func()) {
	return newBusinessAddonAt(manager, filepath.Join("runtime", "business", "config.json"))
}

func newBusinessAddonAt(manager *runtimeManager, path string) (*api.BusinessAddon, func()) {
	ownsLegacy := func(id string) bool {
		generation := manager.generationSnapshot()
		aliases, _ := config.ParseModelMapping(generation.config.ModelMapping)
		for _, alias := range aliases {
			if alias.Alias == id {
				return true
			}
		}
		// trackedService.Models reads its cached snapshot; no refresh or network.
		if service, ok := generation.service.(*trackedService); ok {
			models, _ := service.Models(context.Background())
			for _, model := range models {
				if model.ID == id {
					return true
				}
			}
		}
		return false
	}
	business := host.New(path, ownsLegacy)
	return &api.BusinessAddon{Public: business.PublicHandler(), Admin: business.AdminHandler(), Upload: business.UploadHandler(), OwnsLegacy: ownsLegacy, Principal: business.Principal}, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = business.Close(ctx)
	}
}
