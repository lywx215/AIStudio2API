package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func TestGeminiInlineSDKBase64AndPresence(t *testing.T) {
	data := []byte{0xfb, 0xff, 0x00, 0x01}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		for _, names := range [][2]string{{"inlineData", "mimeType"}, {"inline_data", "mime_type"}} {
			body := fmt.Sprintf(`{"contents":[{"parts":[{"%s":{"%s":"image/png","data":%q}},{"text":""}]}]}`, names[0], names[1], encoding.EncodeToString(data))
			r, err := Parse(strings.NewReader(body), binding())
			if err != nil || len(r.Attachments) != 1 || string(r.Attachments[0].Data) != string(data) || r.Attachments[0].MIMEType != "image/png" || r.QueryParts[0]["text"] != "" {
				t.Fatalf("%s: %+v %v", body, r, err)
			}
			hash := sha256.Sum256(data)
			if r.Attachments[0].Hash != hex.EncodeToString(hash[:]) || !strings.Contains(string(r.Raw["contents"]), names[0]) {
				t.Fatal("original native fields or attachment hash lost")
			}
		}
	}
}

func TestGeminiFilesImmutableSnapshotAndScopedResolver(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request-owned")
	original := []byte("fixture data")
	resolver := func(c context.Context, uri, mime string) (contract.Attachment, error) {
		if c.Value(contextKey{}) != "request-owned" || uri != "files/biz-fixture" || mime != "text/plain" {
			t.Fatal("resolver lost scope")
		}
		return contract.Attachment{Filename: "fixture.txt", MIMEType: mime, Hash: "do-not-trust", Data: original}, nil
	}
	body := `{"contents":[{"parts":[{"text":"read"},{"file_data":{"file_uri":"files/biz-fixture","mime_type":"text/plain"}}]}]}`
	r, err := ParseWithFiles(ctx, strings.NewReader(body), binding(), resolver)
	if err != nil || len(r.Attachments) != 1 {
		t.Fatal(err)
	}
	original[0] = 'X'
	if string(r.Attachments[0].Data) != "fixture data" || r.Attachments[0].Hash == "do-not-trust" {
		t.Fatal("resolver retained mutable ownership")
	}
	if _, err := Parse(strings.NewReader(body), binding()); err == nil {
		t.Fatal("compatibility parser acquired a file store")
	}
	failure := contract.Fail(403, "PERMISSION_DENIED", "Business file belongs to another owner")
	_, err = ParseWithFiles(ctx, strings.NewReader(body), binding(), func(context.Context, string, string) (contract.Attachment, error) {
		return contract.Attachment{}, failure
	})
	if err != failure {
		t.Fatal("resolver authorization error changed", err)
	}
}

func TestGeminiRejectedFilesBeforeResolverOrAccount(t *testing.T) {
	part := `{"inlineData":{"mimeType":"image/png","data":"aGk="}}`
	file := `{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-fixture"}}`
	badParts := []string{
		`{"inlineData":{"mimeType":"audio/wav","data":"aGk="}}`,
		`{"inlineData":{"mimeType":"video/mp4","data":"aGk="}}`,
		`{"inlineData":{"mimeType":"image/gif","data":"aGk="}}`,
		`{"inlineData":{"mimeType":"image/png","data":"aGk=\n"}}`,
		`{"inlineData":{"mimeType":"image/png","data":"Zh=="}}`,
		`{"inlineData":{"mimeType":"image/png","data":""}}`,
		`{"inlineData":{"mimeType":"image/png","mime_type":"image/png","data":"aGk="}}`,
		`{"inlineData":{"mimeType":"image/png","data":"aGk="},"inline_data":{"mimeType":"image/png","data":"aGk="}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"files/old-aistudio"}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"gs://legacy/file"}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"https://example.com/image.png"}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"https://user:pw@example.com/v1beta/files/biz-x"}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-x","file_uri":"files/biz-y"}}`,
		`{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-x"},"thoughtSignature":"opaque"}`,
		`{"text":"signed","thoughtSignature":"opaque"}`,
		`{"text":"before"},` + part + `,{"text":"after"}`,
		file + `,{"text":"middle"},` + file,
	}
	calls, resolves := 0, 0
	h := HandlerWithFiles([]config.Binding{binding()}, generatorFunc(func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error {
		calls++
		return nil
	}), func(context.Context, string, string) (contract.Attachment, error) {
		resolves++
		return contract.Attachment{}, nil
	})
	for _, raw := range badParts {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(`{"contents":[{"parts":[`+raw+`]}]}`)))
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	if calls != 0 || resolves != 0 {
		t.Fatal("invalid fields reached resolver or account", resolves, calls)
	}
}

func TestGeminiFileResolverOriginAndMIMEErrorsBeforeAccount(t *testing.T) {
	calls, resolves := 0, 0
	h := HandlerWithFiles([]config.Binding{binding()}, generatorFunc(func(context.Context, contract.Selection, contract.Request, func(contract.Event) error) error {
		calls++
		return nil
	}), func(context.Context, string, string) (contract.Attachment, error) {
		resolves++
		return contract.Attachment{}, contract.Fail(403, "PERMISSION_DENIED", "External Business file origin rejected")
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", strings.NewReader(`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://external.invalid/v1beta/files/biz-x"}}]}]}`)))
	if w.Code != 403 || resolves != 1 || calls != 0 {
		t.Fatal(w.Code, w.Body.String(), resolves, calls)
	}
	_, err := ParseWithFiles(context.Background(), strings.NewReader(`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"files/biz-x"}}]}]}`), binding(), func(context.Context, string, string) (contract.Attachment, error) {
		return contract.Attachment{MIMEType: "video/mp4", Data: []byte("x")}, nil
	})
	if err == nil {
		t.Fatal("stored MIME mismatch accepted")
	}
}

func TestGeminiFilesCountAndTotalLimits(t *testing.T) {
	inline := `{"inlineData":{"mimeType":"text/plain","data":"eA=="}}`
	accepted, err := Parse(strings.NewReader(`{"contents":[{"parts":[`+strings.TrimSuffix(strings.Repeat(inline+",", 32), ",")+`]}]}`), binding())
	if err != nil || len(accepted.Attachments) != 32 {
		t.Fatal("32 attachments should be supported", err)
	}
	_, err = Parse(strings.NewReader(`{"contents":[{"parts":[`+strings.TrimSuffix(strings.Repeat(inline+",", 33), ",")+`]}]}`), binding())
	if err == nil {
		t.Fatal("too many files accepted")
	}
	_, err = ParseWithFiles(context.Background(), strings.NewReader(`{"contents":[{"parts":[{"fileData":{"mimeType":"application/pdf","fileUri":"files/biz-x"}}]}]}`), binding(), func(context.Context, string, string) (contract.Attachment, error) {
		return contract.Attachment{MIMEType: "application/pdf", Data: make([]byte, maxAttachmentBytes+1)}, nil
	})
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 413 {
		t.Fatal("attachment total not bounded", err)
	}
}

func TestGeminiParameterAliasesSchemaThoughtAndOpaque(t *testing.T) {
	body := `{"contents":[{"parts":[{"text":""}]}],"generation_config":{"temperature":0,"thinking_config":{"includeThoughts":false,"thinkingBudget":0},"response_json_schema":{"properties":{"keep_snake_case":{"const":9007199254740993}}}}}`
	r, err := Parse(strings.NewReader(body), binding())
	if err != nil || string(r.Generation["temperature"]) != "0" || !strings.Contains(string(r.Generation["thinkingConfig"]), "false") || !strings.Contains(string(r.Generation["responseJsonSchema"]), "keep_snake_case") || !strings.Contains(string(r.Generation["responseJsonSchema"]), "9007199254740993") {
		t.Fatal(err, r.Generation)
	}
	for _, config := range []string{
		`"generationConfig":{},"generation_config":{}`,
		`"generationConfig":{"thinkingConfig":{},"thinking_config":{}}`,
		`"generationConfig":{"thinkingConfig":{"includeThoughts":false,"include_thoughts":false}}`,
		`"generationConfig":{"thinkingConfig":{"includeThoughts":null}}`,
	} {
		if _, err := Parse(strings.NewReader(`{"contents":[{"parts":[{"text":"x"}]}],`+config+`}`), binding()); err == nil {
			t.Fatal("accepted", config)
		}
	}
	if !json.Valid(r.Raw["generation_config"]) {
		t.Fatal("original aliases lost")
	}
}
