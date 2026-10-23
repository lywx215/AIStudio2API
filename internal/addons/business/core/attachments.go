package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
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
		id, err := e.uploadAttachment(ctx, account, session, item)
		if err != nil {
			return nil, err
		}
		if id == "" || len(id) > 2048 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return nil, contract.Fail(502, "DATA_LOSS", "Business returned an invalid context file ID")
		}
		seen[key] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func (e *Engine) uploadAttachment(ctx context.Context, account *lease, session string, item contract.Attachment) (string, error) {
	release, err := resources.Reserve(ctx, resources.AttachmentUploadReserveBytes)
	if err != nil {
		return "", err
	}
	defer release()
	body, size, err := attachmentBody(ctx, account.account.Credential.ConfigID, session, item)
	if err != nil {
		return "", err
	}
	response, err := e.postEncoded(ctx, "widgetAddContextFile", account, body, size)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var uploaded struct {
		Response struct {
			FileID string `json:"fileId"`
		} `json:"addContextFileResponse"`
	}
	if err := decodeBounded(response.Body, &uploaded, 1<<20); err != nil {
		return "", err
	}
	return uploaded.Response.FileID, nil
}

// Only the small envelope is marshalled. The attachment itself is encoded on
// demand with a fixed buffer, without a pipe or a background producer.
func attachmentBody(ctx context.Context, config, session string, item contract.Attachment) (io.Reader, int64, error) {
	if len(config)+len(session)+len(item.Filename)+len(item.MIMEType) > 64<<10 {
		return nil, 0, contract.Fail(413, "RESOURCE_EXHAUSTED", "Business attachment metadata exceeds its size limit")
	}
	metadata := struct {
		ConfigID         string            `json:"configId"`
		AdditionalParams map[string]string `json:"additionalParams"`
		File             struct {
			Name     string `json:"name"`
			Filename string `json:"fileName"`
			MIMEType string `json:"mimeType"`
			Contents string `json:"fileContents"`
		} `json:"addContextFileRequest"`
	}{ConfigID: config, AdditionalParams: map[string]string{"token": "-"}}
	metadata.File.Name, metadata.File.Filename, metadata.File.MIMEType = session, item.Filename, item.MIMEType
	prefix, err := json.Marshal(metadata)
	if err != nil {
		return nil, 0, err
	}
	// The final field is an empty string followed by two closing braces.
	prefix = prefix[:len(prefix)-3]
	const suffix = `"}}`
	size := int64(len(prefix)) + int64(base64.StdEncoding.EncodedLen(len(item.Data))) + int64(len(suffix))
	return io.MultiReader(bytes.NewReader(prefix), &attachmentBase64Reader{ctx: ctx, data: item.Data}, strings.NewReader(suffix)), size, nil
}

type attachmentBase64Reader struct {
	ctx     context.Context
	data    []byte
	buffer  [16 << 10]byte
	pending []byte
}

func (r *attachmentBase64Reader) Read(dst []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if len(r.data) == 0 {
			return 0, io.EOF
		}
		count := min(len(r.data), len(r.buffer)/4*3)
		encoded := base64.StdEncoding.EncodedLen(count)
		base64.StdEncoding.Encode(r.buffer[:encoded], r.data[:count])
		r.data = r.data[count:]
		r.pending = r.buffer[:encoded]
	}
	count := copy(dst, r.pending)
	r.pending = r.pending[count:]
	return count, nil
}
