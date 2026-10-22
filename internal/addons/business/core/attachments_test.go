package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

func attachment() contract.Attachment {
	data := []byte("synthetic document")
	hash := sha256.Sum256(data)
	return contract.Attachment{Filename: "fixture.txt", MIMEType: "text/plain", Hash: hex.EncodeToString(hash[:]), Data: data}
}

func TestFilesReuploadWithinEachAccountAndFreshSession(t *testing.T) {
	second := account()
	second.ID = "acct-" + strings.Repeat("b", 32)
	second.Credential.ConfigID = "second-config"
	sessions, uploads, generations := 0, 0, 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/auth/getoxsrf" {
			return reply(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
		}
		if r.URL.Host != "biz-discoveryengine.googleapis.com" || r.Header.Get("Cookie") != "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("attachment credential origin isolation failed")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "widgetCreateSession"):
			sessions++
			return reply(200, fmt.Sprintf(`{"session":{"name":"sessions/%d"}}`, sessions)), nil
		case strings.HasSuffix(r.URL.Path, "widgetAddContextFile"):
			uploads++
			var file map[string]string
			_ = json.Unmarshal(body["addContextFileRequest"], &file)
			data, err := base64.StdEncoding.DecodeString(file["fileContents"])
			if err != nil || string(data) != "synthetic document" || file["name"] != fmt.Sprintf("sessions/%d", sessions) || file["fileName"] != "fixture.txt" || file["mimeType"] != "text/plain" {
				t.Fatal("attachment bytes/session/metadata changed")
			}
			wantConfig := `"synthetic-config"`
			if sessions == 2 {
				wantConfig = `"second-config"`
			}
			if string(body["configId"]) != wantConfig {
				t.Fatal("wrong account used for attachment")
			}
			return reply(200, fmt.Sprintf(`{"addContextFileResponse":{"fileId":"context-%d"}}`, sessions)), nil
		case strings.HasSuffix(r.URL.Path, "widgetStreamAssist"):
			generations++
			var assist struct {
				Session string   `json:"session"`
				FileIDs []string `json:"fileIds"`
			}
			_ = json.Unmarshal(body["streamAssistRequest"], &assist)
			if assist.Session != fmt.Sprintf("sessions/%d", sessions) || len(assist.FileIDs) != 1 || assist.FileIDs[0] != fmt.Sprintf("context-%d", sessions) {
				t.Fatal("stale context file ID reused")
			}
			return reply(200, stream), nil
		default:
			t.Fatal("unexpected upstream action")
			return nil, errors.New("unexpected action")
		}
	})
	e := New(context.Background(), []vault.Account{account(), second}, 2, time.Second, transport)
	defer e.Close()
	input := request()
	input.Attachments = []contract.Attachment{attachment(), attachment()}
	for range 3 {
		if err := e.Generate(context.Background(), contract.Selection{ModelID: "fixture"}, input, func(contract.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if sessions != 3 || uploads != 3 || generations != 3 {
		t.Fatalf("sessions=%d uploads=%d generations=%d", sessions, uploads, generations)
	}
}

func TestInvalidAttachmentRejectedBeforeAccountLease(t *testing.T) {
	for _, modify := range []func(*contract.Attachment){
		func(a *contract.Attachment) { a.Hash = strings.Repeat("0", 64) },
		func(a *contract.Attachment) { a.MIMEType = "audio/wav" },
		func(a *contract.Attachment) { a.Filename = "../fixture.txt" },
		func(a *contract.Attachment) { a.Data = nil },
	} {
		calls := 0
		e := New(context.Background(), nil, 1, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected") }))
		a := attachment()
		modify(&a)
		input := request()
		input.Attachments = []contract.Attachment{a}
		err := e.Generate(context.Background(), contract.Selection{}, input, func(contract.Event) error { return nil })
		var failure *contract.Failure
		if !errors.As(err, &failure) || failure.Code != 400 || calls != 0 {
			t.Fatalf("validation occurred too late: %v", err)
		}
		e.Close()
	}
	tooMany := make([]contract.Attachment, 33)
	for i := range tooMany {
		tooMany[i] = attachment()
	}
	if err := validateAttachments(tooMany); err == nil {
		t.Fatal("attachment count was unbounded")
	}
	big := attachment()
	big.Data = make([]byte, maxAttachmentBytes+1)
	if err := validateAttachments([]contract.Attachment{big}); err == nil {
		t.Fatal("attachment byte budget was unbounded")
	}
}

func TestAttachmentFailureNeverSubmitsGenerationOrReplays(t *testing.T) {
	for _, uploadBody := range []string{"http-failure", `{}`, `{"addContextFileResponse":{"fileId":""}}`} {
		t.Run(uploadBody, func(t *testing.T) {
			calls, uploaded, generated := 0, 0, 0
			e := New(context.Background(), []vault.Account{account()}, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				switch {
				case strings.HasSuffix(r.URL.Path, "getoxsrf"):
					return reply(200, `{"keyId":"fixture","xsrfToken":"c3ludGhldGlj"}`), nil
				case strings.HasSuffix(r.URL.Path, "widgetCreateSession"):
					return reply(200, `{"session":{"name":"sessions/fixture"}}`), nil
				case strings.HasSuffix(r.URL.Path, "widgetAddContextFile"):
					uploaded++
					if uploadBody == "http-failure" {
						return reply(403, `{"secret":"never expose"}`), nil
					}
					return reply(200, uploadBody), nil
				default:
					generated++
					return reply(200, stream), nil
				}
			}))
			defer e.Close()
			input := request()
			input.Attachments = []contract.Attachment{attachment()}
			err := e.Generate(context.Background(), contract.Selection{}, input, func(contract.Event) error { return nil })
			if err == nil || calls != 3 || uploaded != 1 || generated != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("upload failure escaped: calls=%d uploads=%d generated=%d err=%v", calls, uploaded, generated, err)
			}
		})
	}
}
