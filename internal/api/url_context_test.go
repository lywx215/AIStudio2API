package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// Replay actual upstream JSON through the real decoder and Gemini HTTP handler.
type urlMetadataFixtureService struct{ chunks []string }

func (s urlMetadataFixtureService) Models(context.Context) ([]aistudio.Model, error) {
	return []aistudio.Model{{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"}}, nil
}

func (s urlMetadataFixtureService) CountTokens(context.Context, aistudio.TokenCountRequest) (aistudio.TokenCount, error) {
	return aistudio.TokenCount{}, nil
}

func (s urlMetadataFixtureService) Generate(context.Context, aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	decoder := &aistudio.BuildStreamDecoder{}
	var all []aistudio.Event
	for _, chunk := range s.chunks {
		events, err := decoder.Decode([]byte(chunk))
		if err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	if err := decoder.End(); err != nil {
		return nil, err
	}
	out := make(chan aistudio.Event, len(all))
	for _, event := range all {
		out <- event
	}
	close(out)
	return out, nil
}

func TestGeminiURLContextMetadataHTTP(t *testing.T) {
	for _, method := range []string{"generateContent", "streamGenerateContent", "streamGenerateContent?alt=sse"} {
		t.Run(method, func(t *testing.T) {
			service := urlMetadataFixtureService{chunks: []string{
				`{"candidates":[{"content":{"parts":[{"text":"Retrieved document."}]},"urlContextMetadata":{"urlMetadata":[{"retrievedUrl":"https://example.invalid/doc","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_ERROR"},{"retrievedUrl":"https://example.invalid/blocked","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_UNSAFE"}]}}]}`,
				`{"candidates":[{"finishReason":"STOP","urlContextMetadata":{"urlMetadata":[{"retrievedUrl":"https://example.invalid/doc","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS"},{"retrievedUrl":"https://example.invalid/paid","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_PAYWALL"},{"retrievedUrl":"https://example.invalid/future","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_FUTURE"}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`,
			}}
			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:"+method, strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"Read the document"}]}],"tools":[{"url_context":{}}]}`))
			response := httptest.NewRecorder()
			NewHandler(service, Config{}).ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			var chunks []map[string]any
			switch {
			case strings.Contains(method, "alt=sse"):
				scanner := bufio.NewScanner(response.Body)
				for scanner.Scan() {
					if strings.HasPrefix(scanner.Text(), "data: ") {
						var chunk map[string]any
						if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &chunk); err != nil {
							t.Fatal(err)
						}
						chunks = append(chunks, chunk)
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
			case method == "streamGenerateContent":
				if err := json.Unmarshal(response.Body.Bytes(), &chunks); err != nil {
					t.Fatal(err)
				}
			default:
				var chunk map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &chunk); err != nil {
					t.Fatal(err)
				}
				chunks = append(chunks, chunk)
			}
			last := chunks[len(chunks)-1]
			candidate := last["candidates"].([]any)[0].(map[string]any)
			metadata, ok := candidate["urlContextMetadata"].(map[string]any)
			if !ok {
				t.Fatalf("URL retrieval evidence was dropped: %s", response.Body.String())
			}
			entries := metadata["urlMetadata"].([]any)
			if len(entries) != 4 || entries[0].(map[string]any)["urlRetrievalStatus"] != "URL_RETRIEVAL_STATUS_SUCCESS" || entries[1].(map[string]any)["urlRetrievalStatus"] != "URL_RETRIEVAL_STATUS_UNSAFE" || entries[2].(map[string]any)["urlRetrievalStatus"] != "URL_RETRIEVAL_STATUS_PAYWALL" || entries[3].(map[string]any)["urlRetrievalStatus"] != "URL_RETRIEVAL_STATUS_FUTURE" {
				t.Fatalf("retrieval status or streaming evidence lost: %+v", metadata)
			}
			if candidate["finishReason"] != "STOP" || last["usageMetadata"].(map[string]any)["totalTokenCount"] != float64(10) {
				t.Fatalf("existing completion/usage changed: %+v", last)
			}
		})
	}
}

func TestGeminiGroundingCannotFabricateURLContextMetadata(t *testing.T) {
	service := urlMetadataFixtureService{chunks: []string{`{"candidates":[{"content":{"parts":[{"text":"Search result"}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.invalid/doc","title":"Document"}}]}}]}`}}
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:generateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"https://example.invalid/doc"}]}],"tools":[{"googleSearch":{}},{"urlContext":{}}]}`))
	response := httptest.NewRecorder()
	NewHandler(service, Config{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "urlContextMetadata") {
		t.Fatalf("invented URL retrieval evidence: %s", response.Body.String())
	}
}

func TestURLContextMetadataDoesNotAliasUpstream(t *testing.T) {
	input := &aistudio.URLContextMetadata{URLs: []aistudio.URLMetadata{{RetrievedURL: "https://example.invalid/doc", RetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"}}}
	var result generationResult
	if err := result.apply(aistudio.Event{Kind: aistudio.EventURLContext, URLContext: input}); err != nil {
		t.Fatal(err)
	}
	input.URLs[0].RetrievalStatus = "URL_RETRIEVAL_STATUS_ERROR"
	if result.urlContext.URLs[0].RetrievalStatus != "URL_RETRIEVAL_STATUS_SUCCESS" {
		t.Fatal("upstream mutation changed collected evidence")
	}
	empty := geminiURLContextMetadata(aistudio.URLContextMetadata{})
	data, err := json.Marshal(empty)
	if err != nil || string(data) != `{"urlMetadata":[]}` {
		t.Fatalf("empty metadata must not fabricate a URL/status: %s, %v", data, err)
	}
}
