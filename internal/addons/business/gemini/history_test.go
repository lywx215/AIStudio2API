package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/config"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func historyBody(contents ...string) string {
	return `{"contents":[` + strings.Join(contents, ",") + `]}`
}

func textTurn(role, text string) string {
	return fmt.Sprintf(`{"role":%q,"parts":[{"text":%q}]}`, role, text)
}

func TestGeminiHistoryOnlyLatestUserBecomesQuery(t *testing.T) {
	first := textTurn("user", "first hidden value")
	model := `{"role":"model","parts":[{"text":"reply-"},{"text":""},{"text":"one"}]}`
	last := textTurn("user", "follow up")
	single, err := Parse(strings.NewReader(historyBody(first)), binding())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(strings.NewReader(historyBody(first, model, last)), binding())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.History) != 3 || r.History[0] != single.History[0] || r.OptionsHash != single.OptionsHash || len(r.History[0]) != 64 || len(r.OptionsHash) != 64 {
		t.Fatal("invalid history identity", r.History)
	}
	if len(r.QueryParts) != 1 || r.QueryParts[0]["text"] != "follow up" || len(r.Attachments) != 0 {
		t.Fatal("history was replayed or joined into the query", r.QueryParts)
	}
	if r.History[1] != contract.ModelHistoryDigest([]contract.Part{{Text: "reply-one"}}) {
		t.Fatal("SDK text concatenation changed history identity")
	}
	if r.History[1] == r.History[0] {
		t.Fatal("roles not distinguished")
	}
}

func TestGeminiHistoryThoughtSignatureAndSuggestionsBoundaries(t *testing.T) {
	data := []byte(`{"suggestions":["synthetic"],"keep":false}`)
	parts := []contract.Part{
		{Text: "thought", Thought: true, ThoughtSignature: "opaque-one"},
		{Text: "", ThoughtSignature: "opaque-two"},
		{Text: "first"}, {Text: " second"},
		{InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: data}},
		{Text: "tail"},
	}
	wire, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(strings.NewReader(historyBody(textTurn("user", "one"), `{"role":"model","parts":`+string(wire)+`}`, textTurn("user", "two"))), binding())
	if err != nil || r.History[1] != contract.ModelHistoryDigest(parts) {
		t.Fatal("model output cannot round trip unchanged", err)
	}
	mutations := []string{
		strings.Replace(string(wire), "opaque-two", "opaque-other", 1),
		strings.Replace(string(wire), `"thought":true`, `"thought":false`, 1),
		strings.Replace(string(wire), base64.StdEncoding.EncodeToString(data), base64.StdEncoding.EncodeToString([]byte(`{"suggestions":[]}`)), 1),
	}
	for _, changed := range mutations {
		other, err := Parse(strings.NewReader(historyBody(textTurn("user", "one"), `{"role":"model","parts":`+changed+`}`, textTurn("user", "two"))), binding())
		if err != nil || other.History[1] == r.History[1] {
			t.Fatal("changed protected model part retained digest", err)
		}
	}
	withBoundary := contract.ModelHistoryDigest([]contract.Part{{Text: "ab", ThoughtSignature: "x"}, {Text: "c"}})
	withoutBoundary := contract.ModelHistoryDigest([]contract.Part{{Text: "abc", ThoughtSignature: "x"}})
	if withBoundary == withoutBoundary {
		t.Fatal("signed part boundary was flattened")
	}
}

func TestGeminiHistorySDKBase64ReencodingPreservesBytes(t *testing.T) {
	data := []byte(`{"unicode":"中文>??"}`)
	signature := []byte{0xfb, 0xff, 0x00, 0x01}
	parts := []contract.Part{{Text: "", ThoughtSignature: base64.StdEncoding.EncodeToString(signature)}, {InlineData: &contract.InlineData{MIMEType: contract.SuggestionsMIMEType, Data: data}}}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		model := fmt.Sprintf(`{"role":"model","parts":[{"text":"","thought_signature":%q},{"inline_data":{"mime_type":%q,"data":%q}}]}`, encoding.EncodeToString(signature), contract.SuggestionsMIMEType, encoding.EncodeToString(data))
		r, err := Parse(strings.NewReader(historyBody(textTurn("user", "one"), model, textTurn("user", "two"))), binding())
		if err != nil || r.History[1] != contract.ModelHistoryDigest(parts) {
			t.Fatal("SDK protobuf base64 reencoding changed byte identity", err)
		}
	}
}

func TestGeminiHistoryModelPartLimitMatchesOutput(t *testing.T) {
	parts := strings.TrimSuffix(strings.Repeat(`{"text":"x"},`, 4096), ",")
	body := historyBody(textTurn("user", "one"), `{"role":"model","parts":[`+parts+`]}`, textTurn("user", "two"))
	r, err := Parse(strings.NewReader(body), binding())
	if err != nil || r.History[1] != contract.ModelHistoryDigest([]contract.Part{{Text: strings.Repeat("x", 4096)}}) {
		t.Fatal("valid output part count could not be continued", err)
	}
	bad := historyBody(textTurn("user", "one"), `{"role":"model","parts":[`+parts+`,{"text":"x"}]}`, textTurn("user", "two"))
	if _, err := Parse(strings.NewReader(bad), binding()); err == nil {
		t.Fatal("unbounded model part count")
	}
}

func TestGeminiHistoryValidatesRolesAndUnsupportedPartsBeforeFiles(t *testing.T) {
	file := `{"role":"user","parts":[{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-history"}}]}`
	calls := 0
	resolver := func(context.Context, string, string) (contract.Attachment, error) {
		calls++
		return contract.Attachment{MIMEType: "text/plain", Data: []byte("x")}, nil
	}
	bad := []string{
		historyBody(textTurn("model", "first")),
		historyBody(textTurn("user", "one"), textTurn("model", "ends with model")),
		historyBody(file, textTurn("user", "wrong role"), textTurn("user", "two")),
		historyBody(file, `{"parts":[{"text":"missing role"}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"functionCall":{"name":"tool"}}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"text":"x","thought":null}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"text":"x","thoughtSignature":null}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"text":"x","thoughtSignature":"a","thought_signature":"b"}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"text":"x","inlineData":{"mimeType":"application/json+suggestions","data":"e30="}}]}`, textTurn("user", "two")),
		historyBody(file, `{"role":"model","parts":[{"inlineData":{"mimeType":"application/x-unsupported","data":"e30="}}]}`, textTurn("user", "two")),
	}
	for _, body := range bad {
		if _, err := ParseWithFiles(context.Background(), strings.NewReader(body), binding(), resolver); err == nil {
			t.Fatal("invalid history accepted", body)
		}
	}
	if calls != 0 {
		t.Fatal("invalid later turn caused historical file resolution")
	}
}

func TestGeminiHistoryFilesResolveAllOwnersButUploadOnlyLatest(t *testing.T) {
	type scopeKey struct{}
	ctx := context.WithValue(context.Background(), scopeKey{}, "owner")
	file := `{"role":"user","parts":[{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-history"}}]}`
	inline := `{"role":"user","parts":[{"inlineData":{"mimeType":"text/plain","data":"bmV3"}},{"text":"read newest"}]}`
	calls := 0
	resolver := func(ctx context.Context, uri, mime string) (contract.Attachment, error) {
		calls++
		if ctx.Value(scopeKey{}) != "owner" || uri != "files/biz-history" || mime != "text/plain" {
			t.Fatal("historical resolver lost authentication scope")
		}
		return contract.Attachment{Filename: "old.txt", MIMEType: mime, Data: []byte("historical file")}, nil
	}
	first, err := ParseWithFiles(ctx, strings.NewReader(historyBody(file)), binding(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseWithFiles(ctx, strings.NewReader(historyBody(file, textTurn("model", "read"), inline)), binding(), resolver)
	if err != nil || calls != 2 || r.History[0] != first.History[0] {
		t.Fatal(err, calls)
	}
	if len(r.Attachments) != 1 || string(r.Attachments[0].Data) != "new" || r.Attachments[0].Filename != "inline-001.txt" || len(r.QueryParts) != 1 || r.QueryParts[0]["text"] != "read newest" {
		t.Fatal("historical attachment leaked into latest request", r.Attachments)
	}
	latest, err := Parse(strings.NewReader(historyBody(inline)), binding())
	if err != nil || r.History[2] != latest.History[0] {
		t.Fatal("inline ordinals changed across turns", err)
	}
	denied := contract.Fail(403, "PERMISSION_DENIED", "Business file belongs to another owner")
	_, err = ParseWithFiles(ctx, strings.NewReader(historyBody(file, textTurn("model", "read"), inline)), binding(), func(context.Context, string, string) (contract.Attachment, error) {
		return contract.Attachment{}, denied
	})
	if err != denied {
		t.Fatal("historical file authorization bypassed", err)
	}
}

func TestGeminiHistoryLimitsApplyAcrossAllTurns(t *testing.T) {
	contents := make([]string, 33)
	for i := range contents {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		contents[i] = textTurn(role, "x")
	}
	if r, err := Parse(strings.NewReader(historyBody(contents...)), binding()); err != nil || len(r.History) != 33 {
		t.Fatal("33 contents should parse", err)
	}
	contents = append(contents, textTurn("model", "x"), textTurn("user", "x"))
	if _, err := Parse(strings.NewReader(historyBody(contents...)), binding()); err == nil {
		t.Fatal("35 contents accepted")
	}
	_, err := Parse(strings.NewReader(historyBody(textTurn("user", strings.Repeat("a", maxTextBytes/2)), textTurn("model", strings.Repeat("b", maxTextBytes/2)), textTurn("user", "extra"))), binding())
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 413 {
		t.Fatal("aggregate text limit not enforced", err)
	}
	filePart := `{"fileData":{"mimeType":"text/plain","fileUri":"files/biz-history"}}`
	userFiles := func(n int) string {
		return `{"role":"user","parts":[` + strings.TrimSuffix(strings.Repeat(filePart+",", n), ",") + `]}`
	}
	calls := 0
	_, err = ParseWithFiles(context.Background(), strings.NewReader(historyBody(userFiles(16), textTurn("model", "x"), userFiles(17))), binding(), func(context.Context, string, string) (contract.Attachment, error) {
		calls++
		return contract.Attachment{}, nil
	})
	if err == nil || calls != 0 {
		t.Fatal("aggregate attachment count not checked before resolving", err, calls)
	}
	data := make([]byte, 26<<20)
	_, err = ParseWithFiles(context.Background(), strings.NewReader(historyBody(userFiles(1), textTurn("model", "x"), userFiles(1))), binding(), func(ctx context.Context, _, mime string) (contract.Attachment, error) {
		allowance, ok := resources.AttachmentAllowance(ctx)
		expected := int64(50 << 20)
		if calls > 0 {
			expected = 24 << 20
		}
		if !ok || allowance != expected {
			t.Fatal("history attachment allowance not cumulative", allowance)
		}
		calls++
		return contract.Attachment{Filename: "large.txt", MIMEType: mime, Data: data}, nil
	})
	if !errors.As(err, &failure) || failure.Code != 413 || calls != 2 {
		t.Fatal("aggregate attachment bytes not bounded", err, calls)
	}
}

func TestGeminiHistoryOptionsHashCanonicalAndBindingScoped(t *testing.T) {
	parse := func(body string, b config.Binding) string {
		t.Helper()
		r, err := Parse(strings.NewReader(body), b)
		if err != nil {
			t.Fatal(err)
		}
		return r.OptionsHash
	}
	base := `{"contents":[{"parts":[{"text":"one"}]}],"labels":{"b":2,"a":"\u0061"},"generationConfig":{"temperature":0}}`
	equivalent := `{"generationConfig": {"temperature": 0}, "labels":{"a":"a","b":2},"contents":[{"parts":[{"text":"different current user"}]}]}`
	expected := parse(base, binding())
	if parse(equivalent, binding()) != expected {
		t.Fatal("JSON formatting or current query changed option hash")
	}
	for _, changed := range []string{
		strings.Replace(base, `"b":2`, `"b":3`, 1),
		strings.Replace(base, `"temperature":0`, `"temperature":1`, 1),
		strings.Replace(base, `"labels":`, `"ignoredOption":`, 1),
	} {
		if parse(changed, binding()) == expected {
			t.Fatal("changed optional settings retained old scope")
		}
	}
	for _, mutate := range []func(*config.Binding){
		func(b *config.Binding) { b.PublicID = "business-other" },
		func(b *config.Binding) { b.UpstreamModelID = "other" },
		func(b *config.Binding) { b.SelectorMode = "auto" },
		func(b *config.Binding) { b.AccountIDs = []string{"one-account"} },
		func(b *config.Binding) {
			p := b.GenerationParameters["temperature"]
			p.Evidence = "updated"
			b.GenerationParameters["temperature"] = p
		},
	} {
		b := binding()
		mutate(&b)
		if parse(base, b) == expected {
			t.Fatal("changed binding retained old history scope")
		}
	}
}

func TestGeminiHistoryResourceScopeReleased(t *testing.T) {
	body := historyBody(textTurn("user", "one"), textTurn("model", "reply"), textTurn("user", "two"))
	budget := resources.New(2 << 20)
	ctx, lease := budget.Begin(context.Background())
	r, err := ParseWithFiles(ctx, strings.NewReader(body), binding(), nil)
	if err != nil || len(r.History) != 3 || budget.Snapshot().InUseBytes == 0 {
		t.Fatal(err, budget.Snapshot())
	}
	lease.Close()
	if budget.Snapshot().InUseBytes != 0 {
		t.Fatal("history parsing leaked resource reservations")
	}
}
