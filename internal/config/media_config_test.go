package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func isolateConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range configKeys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMediaAndModelChannelsConfigRoundTrip(t *testing.T) {
	isolateConfigEnvironment(t)
	want := Default()
	want.ModelChannels = "synthetic-model:build"
	want.MediaOutput = MediaOutputURL
	want.MediaBaseURL = "https://media.example.invalid/gateway"
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatal("JSON round trip lost configuration fields")
	}
	path := filepath.Join(t.TempDir(), "synthetic.env")
	if err := decoded.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatal("env round trip lost configuration fields")
	}
}

func TestLegacyConfigKeepsInlineMediaDefault(t *testing.T) {
	isolateConfigEnvironment(t)
	raw, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"media_output", "media_base_url", "model_channels"} {
		delete(legacy, field)
	}
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("old JSON config rejected: %v", err)
	}
	if decoded.MediaOutput != MediaOutputInline || decoded.MediaBaseURL != "" || decoded.ModelChannels != "" {
		t.Fatal("old JSON config changed media or routing defaults")
	}
	path := filepath.Join(t.TempDir(), "legacy.env")
	if err := decoded.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MEDIA_") || strings.HasPrefix(line, "MODEL_CHANNELS=") {
			continue
		}
		lines = append(lines, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.MediaOutput != MediaOutputInline {
		t.Fatalf("old env default: mode=%q err=%v", loaded.MediaOutput, err)
	}
}
