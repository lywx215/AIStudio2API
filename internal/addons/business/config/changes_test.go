package config

import "testing"

func TestConfigSnapshotsIsolateNestedRouting(t *testing.T) {
	c := Default()
	c.Bindings[0].AccountIDs = []string{"old-account"}
	c.Bindings[0].GenerationParameters = map[string]ParameterBinding{"temperature": {UpstreamField: "oldField"}}
	copy := c.Clone()
	copy.Bindings[0].DisplayName = "new label"
	copy.Bindings[0].AccountIDs[0] = "new-account"
	copy.Bindings[0].GenerationParameters["temperature"] = ParameterBinding{UpstreamField: "newField"}
	if c.Bindings[0].DisplayName != "Auto" || c.Bindings[0].AccountIDs[0] != "old-account" || c.Bindings[0].GenerationParameters["temperature"].UpstreamField != "oldField" {
		t.Fatal("a new configuration mutated an in-flight routing snapshot")
	}
}

func TestConfigurationChangeClasses(t *testing.T) {
	before := Default()
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want Changes
	}{
		{"revision", func(c *Config) { c.Revision++ }, Changes{}},
		{"metadata", func(c *Config) { c.Bindings[0].DisplayName = "renamed"; c.Bindings[0].Evidence = "observation" }, Changes{Metadata: true}},
		{"mapping", func(c *Config) { c.Bindings[0].AccountIDs = []string{"scope"} }, Changes{Routing: true}},
		{"scheduling", func(c *Config) { c.MaxConcurrent++; c.RequestTimeoutSeconds++ }, Changes{Scheduling: true}},
		{"activation", func(c *Config) { c.Enabled = true }, Changes{Activation: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := before.Clone()
			tc.edit(&after)
			if got := after.ChangesFrom(before); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
