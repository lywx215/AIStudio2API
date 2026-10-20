package aistudio

import (
	"context"
	"testing"
)

func TestURLContextEvidenceSatisfiesRequiredBuiltinTool(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata *URLContextMetadata
		used     bool
	}{
		{"retrieved", &URLContextMetadata{URLs: []URLMetadata{{RetrievedURL: "https://example.invalid/doc", RetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"}}}, true},
		{"failed retrieval was attempted", &URLContextMetadata{URLs: []URLMetadata{{RetrievedURL: "https://example.invalid/doc", RetrievalStatus: "URL_RETRIEVAL_STATUS_ERROR"}}}, true},
		{"empty is not evidence", &URLContextMetadata{}, false},
		{"empty record is not evidence", &URLContextMetadata{URLs: []URLMetadata{{}}}, false},
		{"missing outcome is not evidence", &URLContextMetadata{URLs: []URLMetadata{{RetrievedURL: "https://example.invalid/doc"}}}, false},
		{"unspecified is not evidence", &URLContextMetadata{URLs: []URLMetadata{{RetrievedURL: "https://example.invalid/doc", RetrievalStatus: "URL_RETRIEVAL_STATUS_UNSPECIFIED"}}}, false},
		{"missing is not evidence", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, contract, err := prepareToolRequest(GenerateRequest{Tools: Tools{Google: []string{"url_context"}, ToolConfig: ToolConfig{Mode: "required"}}})
			if err != nil {
				t.Fatal(err)
			}
			source := make(chan Event, 2)
			source <- Event{Kind: EventURLContext, URLContext: test.metadata}
			source <- Event{Kind: EventFinish, FinishReason: "stop"}
			close(source)
			failed := false
			for event := range contract.forward(context.Background(), source) {
				failed = failed || event.Kind == EventError
			}
			if failed == test.used {
				t.Fatalf("required tool failed=%v, used=%v", failed, test.used)
			}
		})
	}
}
