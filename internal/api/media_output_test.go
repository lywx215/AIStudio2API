package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// All media fixtures stay in memory or t.TempDir and never contact an upstream.
type mediaOutputFixtureService struct {
	aistudio.Service
	media    aistudio.Media
	requests chan aistudio.GenerateRequest
}

func (s mediaOutputFixtureService) Generate(_ context.Context, request aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	if s.requests != nil {
		s.requests <- request
	}
	events := make(chan aistudio.Event, 3)
	events <- aistudio.Event{Kind: aistudio.EventText, Text: "Generated media:"}
	events <- aistudio.Event{Kind: aistudio.EventMedia, Media: &s.media}
	events <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "stop"}
	close(events)
	return events, nil
}

func mediaOutputFixture() aistudio.Media {
	return aistudio.Media{MIME: "image/png", Name: "fixture", Data: []byte("synthetic-image-fixture")}
}

func mediaOutputRequest(protocol string, stream bool) (string, string) {
	path := "/v1/chat/completions"
	extra := ""
	if protocol == "messages" {
		path, extra = "/v1/messages", `,"max_tokens":100`
	}
	return path, fmt.Sprintf(`{"model":"public-alias","messages":[{"role":"user","content":"hello"}],"stream":%t%s}`, stream, extra)
}

func TestGeneratedMediaOutputHTTP(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"inline", "url", "request origin"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", protocol, stream, mode), func(t *testing.T) {
					media := mediaOutputFixture()
					dir := t.TempDir()
					configuredBase := "https://media.example.invalid/"
					expectedBase := strings.TrimRight(configuredBase, "/")
					if mode == "request origin" {
						configuredBase, expectedBase = "", "https://client.example.invalid"
					}
					handler := NewHandler(mediaOutputFixtureService{media: media}, Config{
						MediaDir:   dir,
						MediaLinks: func() (bool, string) { return mode != "inline", configuredBase },
					})
					path, body := mediaOutputRequest(protocol, stream)
					request := httptest.NewRequest(http.MethodPost, "https://client.example.invalid"+path, strings.NewReader(body))
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					expected := "data:image/png;base64," + base64.StdEncoding.EncodeToString(media.Data)
					if mode != "inline" {
						expected = expectedBase + "/media/" + mediaFileName(media)
					}
					if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "![fixture]("+expected+")") {
						t.Fatalf("media response=%d %s", response.Code, response.Body)
					}
					if mode == "inline" {
						entries, err := os.ReadDir(dir)
						if err != nil || len(entries) != 0 {
							t.Fatalf("inline output saved media: entries=%d err=%v", len(entries), err)
						}
						return
					}
					download := httptest.NewRecorder()
					handler.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/media/"+mediaFileName(media), nil))
					if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), media.Data) || download.Header().Get("Content-Type") != media.MIME {
						t.Fatalf("saved media readback=%d content-type=%s body=%q", download.Code, download.Header().Get("Content-Type"), download.Body.Bytes())
					}
				})
			}
		}
	}
}

func TestSavedAssistantMediaRestoredWithoutRemoteFetch(t *testing.T) {
	media := mediaOutputFixture()
	dir := t.TempDir()
	if err := (mediaOutput{base: "https://media.example.invalid", dir: dir}).save(media); err != nil {
		t.Fatal(err)
	}
	requests := make(chan aistudio.GenerateRequest, 1)
	handler := NewHandler(mediaOutputFixtureService{media: media, requests: requests}, Config{MediaDir: dir})
	body := fmt.Sprintf(`{"model":"public-alias","messages":[{"role":"assistant","content":"before ![fixture](https://media.example.invalid/media/%s) after"},{"role":"user","content":"describe it"}]}`, mediaFileName(media))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("history response=%d %s", response.Code, response.Body)
	}
	request := <-requests
	if len(request.Contents) != 2 || len(request.Contents[0].Parts) != 3 {
		t.Fatalf("restored history=%+v", request.Contents)
	}
	parts := request.Contents[0].Parts
	if parts[0].Text != "before " || parts[2].Text != " after" || parts[1].InlineData == nil || parts[1].InlineData.MIME != media.MIME || !bytes.Equal(parts[1].InlineData.Data, media.Data) {
		t.Fatalf("restored image parts=%+v", parts)
	}
}

func TestGeneratedMediaSaveErrorsStayPrivate(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				blockedDir := filepath.Join(t.TempDir(), "internal.example.invalid")
				if err := os.WriteFile(blockedDir, []byte("blocks the media directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				handler := NewHandler(mediaOutputFixtureService{media: mediaOutputFixture()}, Config{
					MediaDir:   blockedDir,
					MediaLinks: func() (bool, string) { return true, "https://media.example.invalid" },
				})
				path, body := mediaOutputRequest(protocol, stream)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
				expectedStatus := http.StatusBadGateway
				if stream {
					expectedStatus = http.StatusOK
				}
				if response.Code != expectedStatus || !strings.Contains(response.Body.String(), clientErrorMessage(http.StatusBadGateway, "upstream_error")) {
					t.Fatalf("media failure response=%d %s", response.Code, response.Body)
				}
				assertPrivateErrorAbsent(t, response.Body.String())
			})
		}
	}
}
