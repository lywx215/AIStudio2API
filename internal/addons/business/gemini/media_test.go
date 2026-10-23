package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func TestNativeMediaHistoryRoundTrip(t *testing.T) {
	for _, mime := range []string{"image/png", "audio/L16;codec=pcm;rate=24000", "video/mp4", "application/pdf"} {
		parts := []contract.Part{{Text: "start"}, {InlineData: &contract.InlineData{MIMEType: mime, Data: []byte{255, 0, 254, 127}}, Thought: true, ThoughtSignature: "+/8="}, {Text: "end"}}
		if err := validateOutputPart(parts[1]); err != nil {
			t.Fatal("native media output rejected", err)
		}
		wire, _ := json.Marshal(parts)
		parse := func(value string) (contract.Request, error) {
			return Parse(strings.NewReader(historyBody(textTurn("user", "one"), `{"role":"model","parts":`+value+`}`, textTurn("user", "two"))), binding())
		}
		for _, encoder := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			other := strings.Replace(string(wire), base64.StdEncoding.EncodeToString(parts[1].InlineData.Data), encoder.EncodeToString(parts[1].InlineData.Data), 1)
			parsed, err := parse(other)
			if err != nil || parsed.History[1] != contract.ModelHistoryDigest(parts) || len(parsed.Attachments) != 0 || len(parsed.QueryParts) != 1 || parsed.QueryParts[0]["text"] != "two" {
				t.Fatal("media history altered, uploaded or replayed", mime, err)
			}
		}
		for _, changed := range []string{strings.Replace(string(wire), mime, "image/jpeg", 1), strings.Replace(string(wire), `"thought":true`, `"thought":false`, 1), strings.Replace(string(wire), "/wD+fw==", "/wD+fg==", 1)} {
			parsed, err := parse(changed)
			if err != nil || parsed.History[1] == contract.ModelHistoryDigest(parts) {
				t.Fatal("changed media kept original identity", mime, err)
			}
		}
	}
}

func TestNativeMediaHistoryLimitsAndRejection(t *testing.T) {
	for _, part := range []string{
		`{"inlineData":{"mimeType":"image/svg+xml","data":"YQ=="}}`,
		`{"inlineData":{"mimeType":"image/png","data":""}}`,
		`{"inlineData":{"mimeType":"image/png","data":"/w_-"}}`,
		`{"inlineData":{"mimeType":"application/json+suggestions","data":"e30="},"thought":true}`,
	} {
		_, err := Parse(strings.NewReader(historyBody(textTurn("user", "one"), `{"role":"model","parts":[`+part+`]}`, textTurn("user", "two"))), binding())
		if err == nil {
			t.Fatal("invalid media history accepted")
		}
	}
	data := base64.RawURLEncoding.EncodeToString(make([]byte, (4<<20)*3/7+1))
	body := historyBody(textTurn("user", "one"), `{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"`+data+`"}}]}`, textTurn("user", "two"))
	budget := resources.New(32 << 20)
	ctx, lease := budget.Begin(context.Background())
	_, err := ParseWithFiles(ctx, strings.NewReader(body), binding(), nil)
	lease.Close()
	if err == nil || budget.Snapshot().InUseBytes != 0 {
		t.Fatal("media size bound or request resource cleanup failed", err)
	}
}

func TestNativeMediaFragmentsDoNotConsumeUserAttachmentSlots(t *testing.T) {
	userParts := make([]string, 32)
	for i := range userParts {
		userParts[i] = `{"inlineData":{"mimeType":"text/plain","data":"YQ=="}}`
	}
	parts := make([]contract.Part, 33)
	for i := range parts {
		parts[i].InlineData = &contract.InlineData{MIMEType: "audio/pcm;rate=24000", Data: []byte{0, 255, byte(i)}}
	}
	wire, _ := json.Marshal(parts)
	body := historyBody(`{"role":"user","parts":[`+strings.Join(userParts, ",")+`]}`, `{"role":"model","parts":`+string(wire)+`}`, textTurn("user", "continue"))
	parsed, err := Parse(strings.NewReader(body), binding())
	if err != nil || len(parsed.Attachments) != 0 || parsed.History[1] != contract.ModelHistoryDigest(parts) {
		t.Fatal("delivered media fragments consumed user attachment slots", err)
	}
	body = historyBody(`{"role":"user","parts":[`+strings.Join(append(userParts, userParts[0]), ",")+`]}`, `{"role":"model","parts":`+string(wire)+`}`, textTurn("user", "continue"))
	if _, err := Parse(strings.NewReader(body), binding()); err == nil {
		t.Fatal("33 user attachments accepted")
	}
}
