package gemini

import (
	"bytes"
	"context"
	"encoding/json"
)

// parseGoogleSearch implements only the empty Google Search configuration.
// Unsupported restrictions or other tool variants retain the optional-field
// ignore policy: they must never be reduced to an unrestricted search request.
func parseGoogleSearch(ctx context.Context, raw json.RawMessage) (bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false, nil
	}
	tools, err := rawArray(raw, 64)
	if err != nil {
		return false, err
	}
	enabled, seen := false, false
	for _, value := range tools {
		tool, err := objectContext(ctx, value)
		if err != nil {
			return false, err
		}
		search, exists, err := alias(tool, "googleSearch", "google_search")
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		if seen {
			return false, invalid("Google Search must appear at most once")
		}
		seen = true
		options, err := objectContext(ctx, search)
		if err != nil {
			return false, err
		}
		if len(tool) == 1 && len(options) == 0 {
			enabled = true
		}
	}
	return enabled, nil
}

// ToolConfig can carry retrieval location/language restrictions shared by all
// tools. An empty-only adapter must not discard those and broaden the search.
func googleSearchToolConfigAllows(ctx context.Context, root map[string]json.RawMessage) (bool, error) {
	raw, exists, err := alias(root, "toolConfig", "tool_config")
	if err != nil {
		return false, err
	}
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, nil
	}
	config, err := objectContext(ctx, raw)
	if err != nil {
		return false, err
	}
	return len(config) == 0, nil
}
