// Package gemini owns Business-native Gemini wire types. It does not translate
// requests through OpenAI or the existing AI Studio DTOs.
package gemini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"mime"
	"net/url"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

// object preserves raw values (including 0/false/null and opaque schemas), and
// rejects duplicate keys before typed decoding can collapse their meaning.
func object(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, invalid("expected a JSON object")
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, invalid("invalid JSON")
		}
		name, ok := key.(string)
		if !ok {
			return nil, invalid("invalid JSON key")
		}
		if _, ok := m[name]; ok {
			return nil, invalid("duplicate JSON field")
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, invalid("invalid JSON value")
		}
		m[name] = v
	}
	if _, err = d.Token(); err != nil {
		return nil, invalid("invalid JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, invalid("expected one JSON object")
	}
	return m, nil
}
func invalid(message string) error { return contract.Fail(400, "INVALID_ARGUMENT", message) }
func unsupported(field string) error {
	return invalid("Business does not have a verified mapping for " + field)
}

func Parse(r io.Reader, binding config.Binding) (contract.Request, error) {
	return ParseWithFiles(context.Background(), r, binding, nil)
}

// ParseWithFiles keeps request validation ahead of account selection. Resolvers
// are supplied by the Business host; no legacy AI Studio file pool is consulted.
func ParseWithFiles(ctx context.Context, r io.Reader, binding config.Binding, resolver contract.FileResolver) (contract.Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRequestBytes+1))
	if err != nil {
		return contract.Request{}, invalid("cannot read request body")
	}
	if len(data) > maxRequestBytes {
		return contract.Request{}, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business request exceeds 68 MiB")
	}
	root, err := object(data)
	if err != nil {
		return contract.Request{}, err
	}
	request := contract.Request{Raw: root, Generation: map[string]json.RawMessage{}}
	for field := range root {
		if field != "contents" && field != "generationConfig" && field != "generation_config" {
			return request, unsupported(field)
		}
	}
	generation, hasGeneration, err := alias(root, "generationConfig", "generation_config")
	if err != nil {
		return request, err
	}
	var contents []json.RawMessage
	if json.Unmarshal(root["contents"], &contents) != nil || len(contents) == 0 {
		return request, invalid("contents must contain a user message")
	}
	// Replay of model history is deliberately not converted into prompt text.
	// Enable multi-turn only after session-prefix fidelity is implemented.
	if len(contents) != 1 {
		return request, unsupported("multi-turn contents")
	}
	content, err := object(contents[0])
	if err != nil {
		return request, err
	}
	for key := range content {
		if key != "parts" && key != "role" {
			return request, unsupported("contents." + key)
		}
	}
	if raw, ok := content["role"]; ok {
		var role string
		if json.Unmarshal(raw, &role) != nil || role != "user" {
			return request, invalid("contents[0].role must be user")
		}
	}
	var parts []json.RawMessage
	if json.Unmarshal(content["parts"], &parts) != nil || len(parts) == 0 || len(parts) > 256 {
		return request, invalid("contents[0].parts must contain 1 to 256 parts")
	}
	type pendingAttachment struct {
		inline *contract.Attachment
		uri    string
		mime   string
	}
	var pending []pendingAttachment
	// Context files are global to a fresh Business session. A single contiguous
	// group at either edge has an explicit mapping; interleaving does not.
	groups, previousKind, textBytes := 0, "", 0
	for _, raw := range parts {
		part, err := object(raw)
		if err != nil {
			return request, err
		}
		inline, hasInline, err := alias(part, "inlineData", "inline_data")
		if err != nil {
			return request, err
		}
		file, hasFile, err := alias(part, "fileData", "file_data")
		if err != nil {
			return request, err
		}
		if len(part) != 1 {
			return request, unsupported("mixed or annotated content parts")
		}
		kind := "attachment"
		if text, ok := part["text"]; ok {
			kind = "text"
			var value string
			if bytes.Equal(bytes.TrimSpace(text), []byte("null")) || json.Unmarshal(text, &value) != nil {
				return request, invalid("part.text must be a string")
			}
			textBytes += len(value)
			if textBytes > maxTextBytes {
				return request, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business text exceeds 1 MiB")
			}
			request.QueryParts = append(request.QueryParts, map[string]string{"text": value})
		} else if hasInline {
			a, err := parseInline(inline, len(pending)+1)
			if err != nil {
				return request, err
			}
			pending = append(pending, pendingAttachment{inline: &a})
		} else if hasFile {
			uri, mimeType, err := parseFile(file)
			if err != nil {
				return request, err
			}
			pending = append(pending, pendingAttachment{uri: uri, mime: mimeType})
		} else {
			return request, unsupported("content part")
		}
		if kind != previousKind {
			groups++
			previousKind = kind
		}
		if groups > 2 {
			return request, unsupported("interleaved text and file parts")
		}
		if len(pending) > maxAttachments {
			return request, invalid("Business requests allow at most 32 attachments")
		}
	}
	if len(pending) == 0 && len(data) > maxTextBytes {
		return request, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business text request exceeds 1 MiB")
	}
	if hasGeneration {
		if len(generation) > maxTextBytes {
			return request, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business generationConfig exceeds 1 MiB")
		}
		parameters, err := object(generation)
		if err != nil {
			return request, invalid("generationConfig must be an object")
		}
		parameters, err = parameterAliases(parameters)
		if err != nil {
			return request, err
		}
		if _, first := parameters["responseSchema"]; first {
			if _, second := parameters["responseJsonSchema"]; second {
				return request, invalid("responseSchema and responseJsonSchema cannot be combined")
			}
		}
		for field, value := range parameters {
			mapping, ok := binding.GenerationParameters[field]
			if !ok || !mapping.Verified || strings.TrimSpace(mapping.Evidence) == "" {
				return request, unsupported("generationConfig." + field)
			}
			if source, target := parameterKind(field), parameterKind(mapping.UpstreamField); source != "" && target != "" && source != target {
				return request, invalid("generation parameter mapping has incompatible value types")
			}
			if err := validateParameter(field, value); err != nil {
				return request, err
			}
			if field == "thinkingConfig" {
				value = canonicalThinking(value)
			}
			if _, exists := request.Generation[mapping.UpstreamField]; exists {
				return request, invalid("generation parameter mappings collide")
			}
			request.Generation[mapping.UpstreamField] = append(json.RawMessage(nil), value...)
		}
	}
	// Resolve only after every native field and attachment position passed
	// preflight. The resolver owns same-origin, owner and file-generation checks.
	total := 0
	for _, p := range pending {
		if ctx.Err() != nil {
			return request, contract.Fail(499, "CANCELLED", "Business request cancelled")
		}
		var a contract.Attachment
		if p.inline != nil {
			a = *p.inline
		} else {
			if resolver == nil {
				return request, unsupported("fileData without a Business file resolver")
			}
			a, err = resolver(ctx, p.uri, p.mime)
			if err != nil {
				var failure *contract.Failure
				if errors.As(err, &failure) {
					return request, failure
				}
				return request, invalid("Business file reference could not be resolved")
			}
			resolvedMIME, err := supportedMIME(a.MIMEType)
			if err != nil || resolvedMIME != p.mime {
				return request, invalid("fileData MIME type does not match the Business file")
			}
			a.MIMEType = resolvedMIME
		}
		total += len(a.Data)
		if len(a.Data) == 0 {
			return request, invalid("attachment data must not be empty")
		}
		if total > maxAttachmentBytes {
			return request, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business attachments exceed 50 MiB")
		}
		a.Data = append([]byte(nil), a.Data...)
		hash := sha256.Sum256(a.Data)
		a.Hash = hex.EncodeToString(hash[:])
		request.Attachments = append(request.Attachments, a)
	}
	return request, nil
}

const (
	maxRequestBytes    = 68 << 20
	maxTextBytes       = 1 << 20
	maxAttachmentBytes = 50 << 20
	maxAttachments     = 32
)

func alias(m map[string]json.RawMessage, camel, snake string) (json.RawMessage, bool, error) {
	first, hasFirst := m[camel]
	second, hasSecond := m[snake]
	if hasFirst && hasSecond {
		return nil, false, invalid("conflicting field aliases for " + camel)
	}
	if hasFirst {
		return first, true, nil
	}
	return second, hasSecond, nil
}

func requiredString(raw json.RawMessage, name string) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return "", invalid(name + " must be a nonempty string")
	}
	return value, nil
}

func supportedMIME(value string) (string, error) {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || len(params) != 0 {
		return "", invalid("attachment MIME type must be a media type without parameters")
	}
	supported := strings.HasPrefix(mediaType, "text/")
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/heic", "image/heif", "application/pdf", "application/json", "application/xml":
		supported = true
	}
	if !supported {
		return "", unsupported("attachment MIME type " + mediaType)
	}
	return mediaType, nil
}

func parseInline(raw json.RawMessage, index int) (contract.Attachment, error) {
	var a contract.Attachment
	blob, err := object(raw)
	if err != nil {
		return a, invalid("inlineData must be an object")
	}
	for key := range blob {
		if key != "data" && key != "mimeType" && key != "mime_type" {
			return a, unsupported("inlineData." + key)
		}
	}
	mimeRaw, _, err := alias(blob, "mimeType", "mime_type")
	if err != nil {
		return a, err
	}
	mimeType, err := requiredString(mimeRaw, "inlineData.mimeType")
	if err != nil {
		return a, err
	}
	a.MIMEType, err = supportedMIME(mimeType)
	if err != nil {
		return a, err
	}
	encoded, err := requiredString(blob["data"], "inlineData.data")
	if err != nil {
		return a, err
	}
	// Protobuf JSON accepts both alphabets, with or without padding. Reject
	// whitespace and invalid trailing bits rather than silently repairing them.
	if strings.ContainsAny(encoded, " \t\r\n") {
		return a, invalid("inlineData.data must be valid base64")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding.Strict(), base64.URLEncoding.Strict(), base64.RawStdEncoding.Strict(), base64.RawURLEncoding.Strict()} {
		a.Data, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil || len(a.Data) == 0 {
		return a, invalid("inlineData.data must be nonempty valid base64")
	}
	ext := ".bin"
	switch a.MIMEType {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/heic":
		ext = ".heic"
	case "image/heif":
		ext = ".heif"
	case "application/pdf":
		ext = ".pdf"
	case "application/json":
		ext = ".json"
	case "application/xml":
		ext = ".xml"
	default:
		if strings.HasPrefix(a.MIMEType, "text/") {
			ext = ".txt"
		}
	}
	a.Filename = fmt.Sprintf("inline-%03d%s", index, ext)
	return a, nil
}

func parseFile(raw json.RawMessage) (string, string, error) {
	file, err := object(raw)
	if err != nil {
		return "", "", invalid("fileData must be an object")
	}
	for key := range file {
		if key != "fileUri" && key != "file_uri" && key != "mimeType" && key != "mime_type" {
			return "", "", unsupported("fileData." + key)
		}
	}
	uriRaw, _, err := alias(file, "fileUri", "file_uri")
	if err != nil {
		return "", "", err
	}
	mimeRaw, _, err := alias(file, "mimeType", "mime_type")
	if err != nil {
		return "", "", err
	}
	uri, err := requiredString(uriRaw, "fileData.fileUri")
	if err != nil {
		return "", "", err
	}
	mimeType, err := requiredString(mimeRaw, "fileData.mimeType")
	if err != nil {
		return "", "", err
	}
	mimeType, err = supportedMIME(mimeType)
	if err != nil {
		return "", "", err
	}
	resource := uri
	if strings.Contains(uri, "://") {
		u, err := url.Parse(uri)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/v1beta/") {
			return "", "", invalid("fileData requires a Business file reference")
		}
		resource = strings.TrimPrefix(u.Path, "/v1beta/")
	}
	if !strings.HasPrefix(resource, "files/biz-") || len(strings.TrimPrefix(resource, "files/biz-")) == 0 || strings.ContainsAny(resource, "\\?#% \t\r\n") || strings.Count(resource, "/") != 1 {
		return "", "", invalid("fileData requires a Business file reference")
	}
	return uri, mimeType, nil
}
func validateParameter(field string, value json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return invalid("generationConfig." + field + " cannot be null")
	}
	switch field {
	case "temperature", "topP", "topK", "maxOutputTokens", "candidateCount", "seed", "presencePenalty", "frequencyPenalty":
		var number float64
		if json.Unmarshal(value, &number) != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return invalid("invalid numeric generation parameter")
		}
		exact, ok := new(big.Rat).SetString(string(value))
		if !ok {
			return invalid("invalid numeric generation parameter")
		}
		if (field == "topK" || field == "maxOutputTokens" || field == "candidateCount" || field == "seed") && !exact.IsInt() {
			return invalid("generation parameter requires an integer")
		}
		if field == "candidateCount" && number != 1 {
			return unsupported("candidateCount other than 1")
		}
		if (field == "temperature" || field == "topK" || field == "topP") && number < 0 {
			return invalid("generation parameter cannot be negative")
		}
		if field == "topP" && number > 1 {
			return invalid("topP cannot exceed 1")
		}
		if field == "maxOutputTokens" && number < 1 {
			return invalid("maxOutputTokens must be positive")
		}
	case "stopSequences", "responseModalities":
		var values []string
		if json.Unmarshal(value, &values) != nil || len(values) > 64 {
			return invalid("invalid list generation parameter")
		}
		if field == "responseModalities" && (len(values) != 1 || values[0] != "TEXT") {
			return unsupported("non-text responseModalities")
		}
	case "responseMimeType", "mediaResolution":
		var text string
		if json.Unmarshal(value, &text) != nil || text == "" {
			return invalid("invalid string generation parameter")
		}
	case "responseSchema", "responseJsonSchema":
		if _, err := object(value); err != nil {
			return invalid("generation parameter requires an object")
		}
	case "thinkingConfig":
		parameters, err := object(value)
		if err != nil {
			return invalid("thinkingConfig requires an object")
		}
		for _, names := range [][2]string{{"includeThoughts", "include_thoughts"}, {"thinkingBudget", "thinking_budget"}, {"thinkingLevel", "thinking_level"}} {
			if _, _, err := alias(parameters, names[0], names[1]); err != nil {
				return err
			}
		}
		for key, raw := range parameters {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return invalid("thinkingConfig fields cannot be null")
			}
			switch key {
			case "includeThoughts", "include_thoughts":
				var value bool
				if json.Unmarshal(raw, &value) != nil {
					return invalid("thinkingConfig.includeThoughts must be a boolean")
				}
			case "thinkingBudget", "thinking_budget":
				var value int32
				if json.Unmarshal(raw, &value) != nil || value < -1 {
					return invalid("thinkingConfig.thinkingBudget must be an integer at least -1")
				}
			case "thinkingLevel", "thinking_level":
				var value string
				if json.Unmarshal(raw, &value) != nil || (value != "MINIMAL" && value != "LOW" && value != "MEDIUM" && value != "HIGH") {
					return invalid("unsupported thinkingConfig.thinkingLevel")
				}
			default:
				return unsupported("thinkingConfig." + key)
			}
		}
	default:
		return invalid(fmt.Sprintf("unknown generation parameter %s", field))
	}
	return nil
}

func parameterKind(field string) string {
	switch field {
	case "temperature", "topP", "topK", "maxOutputTokens", "candidateCount", "seed", "presencePenalty", "frequencyPenalty":
		return "number"
	case "stopSequences", "responseModalities":
		return "list"
	case "responseMimeType", "mediaResolution":
		return "string"
	case "responseSchema", "responseJsonSchema", "thinkingConfig":
		return "object"
	}
	return ""
}

func parameterAliases(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	aliases := map[string]string{
		"top_p": "topP", "top_k": "topK", "max_output_tokens": "maxOutputTokens",
		"stop_sequences": "stopSequences", "candidate_count": "candidateCount",
		"presence_penalty": "presencePenalty", "frequency_penalty": "frequencyPenalty",
		"response_mime_type": "responseMimeType", "response_schema": "responseSchema",
		"response_json_schema": "responseJsonSchema", "thinking_config": "thinkingConfig",
		"response_modalities": "responseModalities", "media_resolution": "mediaResolution",
	}
	result := make(map[string]json.RawMessage, len(fields))
	for field, value := range fields {
		canonical := field
		if name, ok := aliases[field]; ok {
			canonical = name
		}
		if _, exists := result[canonical]; exists {
			return nil, invalid("conflicting field aliases for " + canonical)
		}
		result[canonical] = value
	}
	return result, nil
}

func canonicalThinking(raw json.RawMessage) json.RawMessage {
	fields, _ := object(raw) // validateParameter already checked the full object.
	changed := false
	for _, names := range [][2]string{{"includeThoughts", "include_thoughts"}, {"thinkingBudget", "thinking_budget"}, {"thinkingLevel", "thinking_level"}} {
		if value, ok := fields[names[1]]; ok {
			fields[names[0]] = value
			delete(fields, names[1])
			changed = true
		}
	}
	if !changed {
		return raw
	}
	canonical, _ := json.Marshal(fields)
	return canonical
}
