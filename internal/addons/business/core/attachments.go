package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

const maxAttachmentBytes = 50 << 20

// Validate before leasing or authenticating. These limits also protect callers
// of Generator that do not enter through the native Gemini parser.
func validateAttachments(items []contract.Attachment) error {
	if len(items) > 32 {
		return contract.Fail(400, "INVALID_ARGUMENT", "At most 32 Business attachments are supported")
	}
	total := 0
	for _, item := range items {
		total += len(item.Data)
		if total > maxAttachmentBytes {
			return contract.Fail(413, "RESOURCE_EXHAUSTED", "Business attachments exceed the 50 MiB limit")
		}
		media, params, err := mime.ParseMediaType(item.MIMEType)
		allowed := strings.HasPrefix(media, "text/") || media == "application/pdf" || media == "application/json" || media == "application/xml"
		switch media {
		case "image/png", "image/jpeg", "image/webp", "image/heic", "image/heif":
			allowed = true
		}
		if err != nil || len(params) != 0 || !allowed || media != item.MIMEType {
			return contract.Fail(400, "INVALID_ARGUMENT", "Unsupported Business attachment MIME type")
		}
		if len(item.Data) == 0 || len(item.Filename) == 0 || len(item.Filename) > 255 || !utf8.ValidString(item.Filename) || strings.ContainsAny(item.Filename, "/\\") || strings.IndexFunc(item.Filename, unicode.IsControl) >= 0 {
			return contract.Fail(400, "INVALID_ARGUMENT", "Invalid Business attachment snapshot")
		}
		hash := sha256.Sum256(item.Data)
		if item.Hash != "" && item.Hash != hex.EncodeToString(hash[:]) {
			return contract.Fail(400, "INVALID_ARGUMENT", "Business attachment snapshot hash does not match")
		}
	}
	return nil
}

// Uploaded IDs live only in this generation's account lease and fresh session.
// The local Files broker stores bytes, never account/session-specific IDs.
func (e *Engine) uploadAttachments(ctx context.Context, account *lease, session string, items []contract.Attachment) ([]string, error) {
	ids := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := sha256.Sum256(item.Data)
		key := hex.EncodeToString(hash[:]) + "\x00" + item.MIMEType + "\x00" + item.Filename
		if seen[key] {
			continue
		}
		body := map[string]any{
			"configId":         account.account.Credential.ConfigID,
			"additionalParams": map[string]string{"token": "-"},
			"addContextFileRequest": map[string]string{
				"name": session, "fileName": item.Filename, "mimeType": item.MIMEType,
				"fileContents": base64.StdEncoding.EncodeToString(item.Data),
			},
		}
		response, err := e.post(ctx, "widgetAddContextFile", account, body)
		if err != nil {
			return nil, err
		}
		var uploaded struct {
			Response struct {
				FileID string `json:"fileId"`
			} `json:"addContextFileResponse"`
		}
		err = decodeBounded(response.Body, &uploaded, 1<<20)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		id := uploaded.Response.FileID
		if id == "" || len(id) > 2048 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return nil, contract.Fail(502, "DATA_LOSS", "Business returned an invalid context file ID")
		}
		seen[key] = true
		ids = append(ids, id)
	}
	return ids, nil
}
