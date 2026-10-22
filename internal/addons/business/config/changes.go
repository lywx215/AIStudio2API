package config

import (
	"maps"
	"reflect"
	"slices"
)

// Clone gives each HTTP operation a complete, immutable routing snapshot.
func (c Config) Clone() Config {
	c.Bindings = slices.Clone(c.Bindings)
	for i := range c.Bindings {
		c.Bindings[i].AccountIDs = slices.Clone(c.Bindings[i].AccountIDs)
		c.Bindings[i].GenerationParameters = maps.Clone(c.Bindings[i].GenerationParameters)
	}
	return c
}

type Changes struct {
	Metadata   bool
	Routing    bool
	Scheduling bool
	Activation bool
}

// ChangesFrom separates presentation/routing revisions from account lifetimes.
// Credentials have their own vault revision and epoch, outside this document.
func (c Config) ChangesFrom(previous Config) Changes {
	changes := Changes{
		Activation: c.Enabled != previous.Enabled,
		Scheduling: c.MaxConcurrent != previous.MaxConcurrent || c.RequestTimeoutSeconds != previous.RequestTimeoutSeconds,
	}
	old := make(map[string]Binding, len(previous.Bindings))
	for _, b := range previous.Bindings {
		old[b.PublicID] = b
	}
	if len(c.Bindings) != len(previous.Bindings) {
		changes.Routing = true
	}
	for _, b := range c.Bindings {
		before, found := old[b.PublicID]
		if !found {
			changes.Routing = true
			continue
		}
		if b.DisplayName != before.DisplayName || b.Evidence != before.Evidence {
			changes.Metadata = true
		}
		b.DisplayName, b.Evidence = before.DisplayName, before.Evidence
		if !reflect.DeepEqual(b, before) {
			changes.Routing = true
		}
	}
	return changes
}
