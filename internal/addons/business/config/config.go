// Package config owns Business settings. It never edits the host .env file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxBytes = 128 << 10

const (
	DefaultMaxInflightBytes int64 = 256 << 20
	MinInflightBytes        int64 = 64 << 20
	MaxInflightBytes        int64 = 4096 << 20
)

type Binding struct {
	PublicID             string                      `json:"public_id"`
	DisplayName          string                      `json:"display_name"`
	SelectorMode         string                      `json:"selector_mode"`
	UpstreamModelID      string                      `json:"upstream_model_id"`
	Enabled              bool                        `json:"enabled"`
	VerificationStatus   string                      `json:"verification_status"`
	Evidence             string                      `json:"evidence"`
	AccountIDs           []string                    `json:"account_ids,omitempty"`
	GenerationParameters map[string]ParameterBinding `json:"generation_parameters,omitempty"`
}

type ParameterBinding struct {
	UpstreamField string `json:"upstream_field"`
	Verified      bool   `json:"verified"`
	Evidence      string `json:"evidence"`
}

var GenerationFields = map[string]bool{
	"temperature": true, "topP": true, "topK": true, "maxOutputTokens": true,
	"stopSequences": true, "candidateCount": true, "seed": true, "presencePenalty": true, "frequencyPenalty": true,
	"responseMimeType": true, "responseSchema": true, "responseJsonSchema": true,
	"thinkingConfig": true, "responseModalities": true, "mediaResolution": true,
}

type Config struct {
	SchemaVersion         int       `json:"schema_version"`
	Revision              uint64    `json:"revision"`
	Enabled               bool      `json:"enabled"`
	MaxConcurrent         int       `json:"max_concurrent"`
	MaxInflightBytes      int64     `json:"max_inflight_bytes"`
	RequestTimeoutSeconds int       `json:"request_timeout_seconds"`
	Bindings              []Binding `json:"bindings"`
}

func Default() Config {
	c := Config{SchemaVersion: 1, MaxConcurrent: 2, MaxInflightBytes: DefaultMaxInflightBytes, RequestTimeoutSeconds: 120, Bindings: []Binding{}}
	for _, item := range [][2]string{
		{"auto", "Auto"}, {"gemini-4-argon", "Gemini 4 Argon"}, {"gemini-3.8-flash", "Gemini 3.8 Flash"},
		{"gemini-3.7-flash", "Gemini 3.7 Flash"}, {"gemini-3.6-flash", "Gemini 3.6 Flash"},
		{"gemini-3.5-flash", "Gemini 3.5 Flash"}, {"gemini-3.1-pro", "Gemini 3.1 Pro"}, {"gemini-2.5-pro", "Gemini 2.5 Pro"},
	} {
		mode := "unresolved"
		if item[0] == "auto" {
			mode = "auto"
		}
		c.Bindings = append(c.Bindings, Binding{PublicID: "business-" + item[0], DisplayName: item[1], SelectorMode: mode, VerificationStatus: "unverified"})
	}
	return c
}

var publicID = regexp.MustCompile(`^business-[a-z0-9][a-z0-9.-]{0,100}$`)
var upstreamField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// Validate refuses guessed/ambiguous routing. Evidence is an administrator's
// observation reference, not a cookie, token, or proof of live certification.
func (c Config) Validate(ownsLegacy func(string) bool) error {
	if c.SchemaVersion != 1 {
		return errors.New("schema_version must be 1")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 16 {
		return errors.New("max_concurrent must be between 1 and 16")
	}
	if c.MaxInflightBytes < MinInflightBytes || c.MaxInflightBytes > MaxInflightBytes || c.MaxInflightBytes%(1<<20) != 0 {
		return errors.New("max_inflight_bytes must be an integer MiB between 64 and 4096 MiB")
	}
	if c.RequestTimeoutSeconds < 10 || c.RequestTimeoutSeconds > 300 {
		return errors.New("request_timeout_seconds must be between 10 and 300")
	}
	if len(c.Bindings) > 128 {
		return errors.New("at most 128 model bindings are allowed")
	}
	seen := make(map[string]bool)
	for _, b := range c.Bindings {
		if !publicID.MatchString(b.PublicID) || seen[b.PublicID] {
			return errors.New("public_id must be unique and use the business- namespace")
		}
		seen[b.PublicID] = true
		if strings.TrimSpace(b.DisplayName) == "" || len(b.DisplayName) > 160 {
			return errors.New("display_name is required and limited to 160 bytes")
		}
		if len(b.UpstreamModelID) > 160 || strings.ContainsAny(b.UpstreamModelID, "\r\n\t /\\") {
			return errors.New("invalid upstream_model_id")
		}
		if b.SelectorMode != "exact" && b.SelectorMode != "auto" && b.SelectorMode != "unresolved" {
			return errors.New("selector_mode must be exact, auto or unresolved")
		}
		if b.SelectorMode == "exact" && b.UpstreamModelID == "" {
			return errors.New("exact selection requires upstream_model_id")
		}
		if b.SelectorMode != "exact" && b.UpstreamModelID != "" {
			return errors.New("only exact selection may specify upstream_model_id")
		}
		if b.VerificationStatus != "verified" && b.VerificationStatus != "unverified" {
			return errors.New("invalid verification_status")
		}
		if len(b.Evidence) > 1024 {
			return errors.New("evidence is limited to 1024 bytes")
		}
		if b.Enabled && (b.SelectorMode == "unresolved" || b.VerificationStatus != "verified" || strings.TrimSpace(b.Evidence) == "") {
			return errors.New("enabled bindings require a resolved selector and recorded verification evidence")
		}
		if b.Enabled && ownsLegacy != nil && ownsLegacy(b.PublicID) {
			return errors.New("an enabled binding conflicts with an existing AI Studio alias")
		}
		if len(b.AccountIDs) > 128 {
			return errors.New("too many account scopes")
		}
		for _, id := range b.AccountIDs {
			if len(id) != 37 || !strings.HasPrefix(id, "acct-") {
				return errors.New("invalid account scope")
			}
		}
		targets := map[string]bool{}
		for field, p := range b.GenerationParameters {
			if !GenerationFields[field] || !upstreamField.MatchString(p.UpstreamField) || p.UpstreamField == "modelId" || len(p.Evidence) > 1024 {
				return errors.New("invalid generation parameter mapping")
			}
			if p.Verified && strings.TrimSpace(p.Evidence) == "" {
				return errors.New("verified parameter mappings require evidence")
			}
			if targets[p.UpstreamField] {
				return errors.New("generation parameter mappings must have distinct targets")
			}
			targets[p.UpstreamField] = true
		}
	}
	return nil
}

func Decode(r io.Reader) (Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Config{}, errors.New("cannot read Business configuration")
	}
	if len(data) > MaxBytes {
		return Config{}, errors.New("Business configuration is too large")
	}
	// Existing schema-1 documents predate this field. Only omission receives
	// the default; an explicit zero/null must not silently disable admission.
	c := Config{MaxInflightBytes: DefaultMaxInflightBytes}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Config{}, errors.New("invalid Business configuration JSON or unknown field")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("expected one JSON document")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) == nil && bytes.Equal(bytes.TrimSpace(fields["max_inflight_bytes"]), []byte("null")) {
		return Config{}, errors.New("max_inflight_bytes must be a number")
	}
	return c, nil
}

func Load(path string, ownsLegacy func(string) bool) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, errors.New("BUSINESS_CONFIG_UNREADABLE")
	}
	defer f.Close()
	c, err := Decode(f)
	if err != nil {
		return Config{}, errors.New("BUSINESS_CONFIG_INVALID")
	}
	if err := c.Validate(ownsLegacy); err != nil {
		return Config{}, errors.New("BUSINESS_CONFIG_INVALID")
	}
	return c, nil
}

// Save atomically replaces this module's settings. Callers serialize writers
// and check Revision first. No storage is created by Default or Load.
func Save(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("BUSINESS_CONFIG_WRITE_FAILED")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return errors.New("BUSINESS_CONFIG_WRITE_FAILED")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.New("BUSINESS_CONFIG_WRITE_FAILED")
	}
	return nil
}
