package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

const (
	StageConfigLookup    ErrorStage = "config_lookup"
	maxWidgetConfigBytes            = 1 << 20
	googleSearchProvider            = "WEB_GROUNDING_TYPE_GOOGLE_SEARCH"
)

// requireGoogleSearch checks the current immutable account lease immediately
// before session creation or reuse. The provider is not cached: a formerly
// enabled Google Search account may now use Enterprise Web Search or be disabled.
func (e *Engine) requireGoogleSearch(ctx context.Context, a *lease) error {
	response, err := e.post(ctx, "lookupWidgetConfig", a, map[string]string{
		"widgetConfigId": a.account.Credential.ConfigID,
		"modelInfoView":  "END_USER_WEB",
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxWidgetConfigBytes+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return &transportError{err}
	}
	if len(raw) > maxWidgetConfigBytes || validateStreamFrame(raw) != nil {
		return malformedWidgetConfig()
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return malformedWidgetConfig()
	}
	if rpcError := envelope["error"]; len(rpcError) != 0 && !bytes.Equal(bytes.TrimSpace(rpcError), []byte("null")) {
		// Configuration responses allow 1 MiB, but the shared RPC parser is
		// deliberately bounded to 64 KiB. An oversized error must not disappear
		// into the provider path, including envelopes that also contain config.
		if len(raw) > maxErrorBody {
			return malformedWidgetConfig()
		}
		if failure, ok := rpcEnvelopeFailure(raw); ok {
			return failure
		}
		return malformedWidgetConfig()
	}
	// Decode exact case-sensitive names. Struct decoding would also accept
	// alternate casing, and ordinary map decoding alone would hide duplicate keys.
	for _, name := range []string{"anonymousWidgetConfig", "assistantSettings", "webGroundingType"} {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return malformedWidgetConfig()
		}
		raw = object[name]
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return googleSearchUnavailable()
		}
	}
	var provider string
	if json.Unmarshal(raw, &provider) != nil {
		return malformedWidgetConfig()
	}
	if provider != googleSearchProvider {
		return googleSearchUnavailable()
	}
	return nil
}

func googleSearchUnavailable() error {
	return contract.Fail(400, "INVALID_ARGUMENT", "Google Search is not enabled for this Business account")
}

func malformedWidgetConfig() error {
	return &protocolError{errors.New("invalid Business widget configuration")}
}
