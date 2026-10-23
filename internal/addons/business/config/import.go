package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ImportPreview contains only sanitized model configuration. Neither the raw
// source nor account scopes or imported verification claims are returned.
type ImportPreview struct {
	Revision   uint64    `json:"revision"`
	SourceHash string    `json:"source_hash"`
	Added      []Binding `json:"added"`
	Skipped    []string  `json:"skipped"`
	Warnings   []string  `json:"warnings"`
}

func invalidImport() error { return errors.New("Invalid Business model import source") }

// PreviewImport is pure: only an explicit models/bindings projection is
// accepted. Full foreign account/config files are intentionally rejected.
func PreviewImport(current Config, source []byte) (ImportPreview, error) {
	preview := ImportPreview{Revision: current.Revision, Added: []Binding{}, Skipped: []string{}, Warnings: []string{}}
	if len(source) > MaxBytes || ValidateImportJSON(source) != nil {
		return preview, invalidImport()
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(source, &root) != nil || len(root) != 1 {
		return preview, invalidImport()
	}
	var entries []json.RawMessage
	pool := false
	if raw, ok := root["models"]; ok {
		pool = true
		if json.Unmarshal(raw, &entries) != nil {
			return preview, invalidImport()
		}
	} else if raw, ok := root["bindings"]; ok {
		if json.Unmarshal(raw, &entries) != nil {
			return preview, invalidImport()
		}
	} else {
		return preview, invalidImport()
	}
	if len(entries) == 0 || len(entries) > 128 {
		return preview, invalidImport()
	}
	var compact bytes.Buffer
	if json.Compact(&compact, source) != nil {
		return preview, invalidImport()
	}
	sum := sha256.Sum256(compact.Bytes())
	preview.SourceHash = hex.EncodeToString(sum[:])
	existing := make(map[string]bool, len(current.Bindings))
	for _, binding := range current.Bindings {
		existing[binding.PublicID] = true
	}
	seen := map[string]bool{}
	for _, raw := range entries {
		binding, err := importBinding(raw, pool)
		if err != nil || seen[binding.PublicID] {
			return ImportPreview{}, invalidImport()
		}
		seen[binding.PublicID] = true
		if existing[binding.PublicID] {
			preview.Skipped = append(preview.Skipped, binding.PublicID)
			continue
		}
		preview.Added = append(preview.Added, binding)
	}
	candidate := current.Clone()
	if candidate.Revision == ^uint64(0) {
		return ImportPreview{}, invalidImport()
	}
	candidate.Revision++
	candidate.Bindings = append(candidate.Bindings, preview.Added...)
	if candidate.Validate(nil) != nil {
		return ImportPreview{}, invalidImport()
	}
	if !ImportFitsStorage(candidate) {
		return ImportPreview{}, invalidImport()
	}
	preview.Warnings = append(preview.Warnings, "Imported models are disabled and unverified; account scopes and verification evidence are removed.")
	if len(preview.Skipped) > 0 {
		preview.Warnings = append(preview.Warnings, "Existing model IDs are skipped without changes; rename source IDs to import separate entries.")
	}
	if pool {
		preview.Warnings = append(preview.Warnings, "Pool description and context/token limits are not imported or treated as capability evidence.")
	}
	if len(preview.Added) == 0 {
		preview.Warnings = append(preview.Warnings, "No new model bindings can be applied.")
	}
	return preview, nil
}

// ImportFitsStorage checks the final revision and Save's exact encoding; the
// rollback path uses this too because increasing revision can add digits.
func ImportFitsStorage(candidate Config) bool {
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	return err == nil && len(encoded)+1 <= MaxBytes
}

func importBinding(raw json.RawMessage, pool bool) (Binding, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return Binding{}, invalidImport()
	}
	var binding Binding
	if pool {
		allowed := map[string]bool{"id": true, "name": true, "description": true, "routing_mode": true, "upstream_model_id": true, "enabled": true, "context_length": true, "max_tokens": true}
		for key := range fields {
			if !allowed[key] {
				return binding, invalidImport()
			}
		}
		for _, key := range []string{"id", "name", "description", "routing_mode", "upstream_model_id", "enabled"} {
			if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
				return binding, invalidImport()
			}
		}
		var value struct {
			ID              string       `json:"id"`
			Name            string       `json:"name"`
			Description     string       `json:"description"`
			RoutingMode     *string      `json:"routing_mode"`
			UpstreamModelID *string      `json:"upstream_model_id"`
			Enabled         *bool        `json:"enabled"`
			ContextLength   *json.Number `json:"context_length"`
			MaxTokens       *json.Number `json:"max_tokens"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return binding, invalidImport()
		}
		binding.PublicID, binding.DisplayName = strings.TrimSpace(value.ID), strings.TrimSpace(value.Name)
		if !strings.HasPrefix(binding.PublicID, "business-") {
			binding.PublicID = "business-" + binding.PublicID
		}
		mode := "explicit"
		if value.RoutingMode != nil {
			mode = *value.RoutingMode
		}
		switch mode {
		case "explicit":
			binding.SelectorMode = "exact"
			// The reference model_routing.py explicitly defines this legacy
			// fallback. It is retained as an unverified, disabled candidate.
			binding.UpstreamModelID = strings.TrimSpace(value.ID)
			if value.UpstreamModelID != nil {
				binding.UpstreamModelID = strings.TrimSpace(*value.UpstreamModelID)
			}
		case "auto":
			binding.SelectorMode = "auto"
		case "unconfigured":
			binding.SelectorMode = "unresolved"
		default:
			return binding, invalidImport()
		}
	} else {
		allowed := map[string]bool{"public_id": true, "display_name": true, "selector_mode": true, "upstream_model_id": true, "enabled": true, "verification_status": true, "evidence": true, "account_ids": true, "generation_parameters": true}
		for key := range fields {
			if !allowed[key] {
				return binding, invalidImport()
			}
		}
		if rawParameters, exists := fields["generation_parameters"]; exists {
			var parameters map[string]map[string]json.RawMessage
			if json.Unmarshal(rawParameters, &parameters) != nil {
				return binding, invalidImport()
			}
			for _, parameter := range parameters {
				if parameter == nil {
					return binding, invalidImport()
				}
				for key := range parameter {
					if key != "upstream_field" && key != "verified" && key != "evidence" {
						return binding, invalidImport()
					}
				}
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&binding) != nil {
			return binding, invalidImport()
		}
	}
	binding.Enabled, binding.VerificationStatus, binding.Evidence, binding.AccountIDs = false, "unverified", "", nil
	for key, parameter := range binding.GenerationParameters {
		parameter.Verified, parameter.Evidence = false, ""
		binding.GenerationParameters[key] = parameter
	}
	test := Default()
	test.Bindings = []Binding{binding}
	if test.Validate(nil) != nil {
		return Binding{}, invalidImport()
	}
	return binding, nil
}

// ValidateImportJSON bounds nesting and rejects duplicate keys and Unicode
// replacement before typed decoding. Errors never include source values.
func ValidateImportJSON(raw []byte) error {
	if len(raw) == 0 || !utf8.Valid(raw) || !validImportEscapes(raw) {
		return invalidImport()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return invalidImport()
	}
	if validateImportContainer(decoder, '{', 1) != nil {
		return invalidImport()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalidImport()
	}
	return nil
}

func validateImportContainer(decoder *json.Decoder, kind json.Delim, depth int) error {
	if depth > 32 {
		return invalidImport()
	}
	keys := map[string]bool{}
	for decoder.More() {
		if kind == '{' {
			value, err := decoder.Token()
			key, ok := value.(string)
			if err != nil || !ok || keys[key] {
				return invalidImport()
			}
			keys[key] = true
		}
		token, err := decoder.Token()
		if err != nil {
			return invalidImport()
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter != '{' && delimiter != '[' {
				return invalidImport()
			}
			if validateImportContainer(decoder, delimiter, depth+1) != nil {
				return invalidImport()
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || (kind == '{' && end != json.Delim('}')) || (kind == '[' && end != json.Delim(']')) {
		return invalidImport()
	}
	return nil
}

func validImportEscapes(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return false
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if err != nil || (value >= 0xdc00 && value <= 0xdfff) {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return !inString
}
