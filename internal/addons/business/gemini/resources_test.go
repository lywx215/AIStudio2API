package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

type budgetBodySpy struct{ reads int }

func (s *budgetBodySpy) Read([]byte) (int, error) { s.reads++; return 0, io.EOF }

func TestUnknownLengthGenerationRejectsFullBudgetBeforeBody(t *testing.T) {
	b := resources.New(0)
	ctx, scope := b.Begin(context.Background())
	defer scope.Close()
	spy := &budgetBodySpy{}
	r := httptest.NewRequest("POST", "/v1beta/models/business-test:generateContent", spy).WithContext(ctx)
	r.ContentLength = -1
	w := httptest.NewRecorder()
	Handler([]config.Binding{binding()}, nil).ServeHTTP(w, r)
	if w.Code != 429 || spy.reads != 0 {
		t.Fatal(w.Code, spy.reads, w.Body.String())
	}
}

func TestObjectViewsRetainRawAndBoundFieldCount(t *testing.T) {
	raw := []byte(`{"x":{"opaque":[0,false,null]},"y":"base64"}`)
	obj, err := object(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(obj["x"]) != `{"opaque":[0,false,null]}` {
		t.Fatal(string(obj["x"]))
	}
	raw[strings.Index(string(raw), "opaque")] = 'O'
	if !strings.Contains(string(obj["x"]), "Opaque") {
		t.Fatal("raw value was copied")
	}
	fields := make([]string, 257)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"k%d":0`, i)
	}
	_, err = object([]byte("{" + strings.Join(fields, ",") + "}"))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 413 {
		t.Fatal(err)
	}
}

func TestNestedObjectBudgetFailuresRemain429(t *testing.T) {
	for _, parse := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := objectContext(ctx, []byte(`{"temperature":0}`)); return err },
		func(ctx context.Context) error {
			_, _, err := inlineFields(ctx, json.RawMessage(`{"mimeType":"text/plain","data":"eA=="}`))
			return err
		},
		func(ctx context.Context) error {
			_, _, err := parseFile(ctx, json.RawMessage(`{"mimeType":"text/plain","fileUri":"files/biz-x"}`))
			return err
		},
	} {
		b := resources.New(0)
		ctx, scope := b.Begin(context.Background())
		err := parse(ctx)
		if !errors.Is(err, resources.ErrExhausted) {
			t.Fatal(err)
		}
		scope.Close()
	}
}

func TestInlineCumulativeLimitPrecedesEncodedStringAllocation(t *testing.T) {
	b := resources.New(32)
	ctx, scope := b.Begin(context.Background())
	defer scope.Close()
	_, err := parseInline(ctx, json.RawMessage(`{"mimeType":"text/plain","data":"`+strings.Repeat("eHh4", 100)+`"}`), 1, 1)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 413 {
		t.Fatal("allocated encoded string before clear per-request rejection", err)
	}
}

func TestParserKeepsOriginalContentsAndReleasesAtScopeEnd(t *testing.T) {
	body := `{"contents":[{"parts":[{"inline_data":{"mime_type":"text/plain","data":"eA=="}}]}]}`
	b := resources.New(2 << 20)
	ctx, scope := b.Begin(context.Background())
	parsed, err := ParseWithFiles(ctx, strings.NewReader(body), binding(), nil)
	if err != nil || !strings.Contains(string(parsed.Raw["contents"]), `"inline_data"`) || string(parsed.Attachments[0].Data) != "x" {
		t.Fatal(parsed, err)
	}
	if b.Snapshot().InUseBytes < int64(len(body)+1) {
		t.Fatal("raw/decoded buffers not retained in budget")
	}
	scope.Close()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("parser scope leaked")
	}
}

func TestResponseMarshalAdmitsWorstCaseEscapesBeforeEncoding(t *testing.T) {
	b := resources.New(4096)
	ctx, scope := b.Begin(context.Background())
	defer scope.Close()
	if _, _, err := marshalResponse(ctx, []contract.Part{{Text: strings.Repeat("\x00", 1000)}}, true); !errors.Is(err, resources.ErrExhausted) {
		t.Fatal(err)
	}
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("failed response leaked")
	}
	b.SetLimit(100000)
	data, release, err := marshalResponse(ctx, []contract.Part{{Text: "\x00"}}, true)
	if err != nil || !json.Valid(data) {
		t.Fatal(err)
	}
	if b.Snapshot().InUseBytes == 0 {
		t.Fatal("encoded bytes released before write")
	}
	release()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("response release leaked")
	}
}
