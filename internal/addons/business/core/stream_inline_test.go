package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
)

func streamMediaPart(mime, encoded string) map[string]any {
	return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": encoded}}
}

// Synthetic binary fragments test transport fidelity, not a model's ability
// to produce any particular container or modality. No container is decoded.
func TestStreamInlineMediaPreservesBytesMIMEAndAnnotations(t *testing.T) {
	data := []byte{0xff, 0, 0xfe, 0xfb, 0x80}
	for _, mime := range []string{"image/png", "IMAGE/JPEG", "image/webp", "image/gif", "image/heic", "image/heif", "audio/wav", "audio/x-wav", "audio/mp3", "audio/mpeg", "audio/mp4", "audio/aac", "audio/ogg", "audio/flac", "audio/webm", "audio/pcm", "audio/L16;rate=24000;channels=1;codec=pcm", "video/mp4", "video/webm", "video/mpeg", "video/quicktime", "application/pdf"} {
		t.Run(mime, func(t *testing.T) {
			part := streamMediaPart(mime, base64.StdEncoding.EncodeToString(data))
			part["role"], part["thought"], part["thoughtSignature"] = "model", true, "opaque+/=签名"
			raw, err := json.Marshal([]any{liveFrame("SUCCEEDED", part)})
			if err != nil {
				t.Fatal(err)
			}
			var parts []contract.Part
			finished := 0
			err = decodeStream(&oneByteReader{data: raw}, func(event contract.Event) error {
				parts = append(parts, event.Parts...)
				if event.Finished {
					finished++
				}
				return nil
			})
			want := contract.Part{InlineData: &contract.InlineData{MIMEType: mime, Data: data}, Thought: true, ThoughtSignature: "opaque+/=签名"}
			if err != nil || finished != 1 || len(parts) != 1 || !reflect.DeepEqual(parts[0], want) {
				t.Fatal("binary bytes, original MIME or annotations changed across reader boundaries", finished, err)
			}
			wire, err := json.Marshal(parts[0])
			if err != nil || bytes.Contains(wire, []byte(`"text"`)) || bytes.Contains(wire, []byte(`"functionCall"`)) || !bytes.Contains(wire, []byte(base64.StdEncoding.EncodeToString(data))) {
				t.Fatal("media acquired a synthetic value or lost canonical bytes", err)
			}
		})
	}
}

func TestStreamInlineProtoJSONBase64Variants(t *testing.T) {
	for _, sample := range []struct {
		mime string
		data []byte
	}{
		{"image/png", []byte{0xfb, 0xff}},
		{"audio/L16;rate=24000", []byte{0xfb, 0xff, 0, 1}},
		{contract.SuggestionsMIMEType, []byte(`"ÿ"`)},
		{contract.SuggestionsMIMEType, []byte(`{}`)},
	} {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			parts, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", streamMediaPart(sample.mime, encoding.EncodeToString(sample.data))))
			if err != nil || !finished || len(parts) != 1 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != sample.mime || !bytes.Equal(parts[0].InlineData.Data, sample.data) {
				t.Fatal("legal ProtoJSON byte representation changed data", sample.mime, finished, err)
			}
		}
	}
	for _, value := range []string{"", "A", "A=", "AA=", "A===", "AA=A", "AA==x", "AA===", "AB==", "AB", "AAB=", "AAB", "+_8=", "-/8=", "+/8=\n", "+/8=\r", "+/8=\t", " +/8=", "+/8=\v", "éA=="} {
		_, finished, err := decodeLiveFixture(t, liveFrame("SUCCEEDED", streamMediaPart("image/png", value)))
		if err == nil || finished {
			t.Fatal("noncanonical bits, mixed alphabet, bad padding or whitespace accepted", finished)
		}
	}
}

func TestStreamInlineMalformedValuesFailAfterPriorOutput(t *testing.T) {
	for name, content := range map[string]string{
		"null-blob":             `{"inlineData":null}`,
		"null-data":             `{"inlineData":{"mimeType":"image/png","data":null}}`,
		"numeric-data":          `{"inlineData":{"mimeType":"image/png","data":1}}`,
		"unknown-blob-field":    `{"inlineData":{"mimeType":"image/png","data":"AA==","uri":"https://example.test"}}`,
		"duplicate-blob-key":    `{"inlineData":{"mimeType":"image/png","data":"AA==","data":"AQ=="}}`,
		"active-mime":           `{"inlineData":{"mimeType":"image/svg+xml","data":"AA=="}}`,
		"unknown-mime":          `{"inlineData":{"mimeType":"application/octet-stream","data":"AA=="}}`,
		"unknown-parameter":     `{"inlineData":{"mimeType":"audio/pcm;filename=x","data":"AA=="}}`,
		"mixed-union":           `{"inlineData":{"mimeType":"image/png","data":"AA=="},"text":""}`,
		"thought-null":          `{"inlineData":{"mimeType":"image/png","data":"AA=="},"thought":null}`,
		"signature-null":        `{"inlineData":{"mimeType":"image/png","data":"AA=="},"thoughtSignature":null}`,
		"suggestions-thought":   `{"inlineData":{"mimeType":"application/json+suggestions","data":"e30="},"thought":false}`,
		"suggestions-signature": `{"inlineData":{"mimeType":"application/json+suggestions","data":"e30="},"thoughtSignature":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			first, err := json.Marshal(liveFrame("IN_PROGRESS", streamMediaPart("audio/pcm", "AA==")))
			if err != nil {
				t.Fatal(err)
			}
			body := "[" + string(first) + `,{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":` + content + `}}]}}}]`
			parts, finished := 0, false
			err = decodeStream(strings.NewReader(body), func(event contract.Event) error {
				parts += len(event.Parts)
				finished = finished || event.Finished
				return nil
			})
			if err == nil || parts != 1 || finished {
				t.Fatal("malformed media was forwarded or certified success after output", parts, finished, err)
			}
		})
	}
}

func TestStreamInlineCanonicalByteBudgetAndCumulativeAccounting(t *testing.T) {
	const limit = 4 << 20
	n := limit * 3 / 7
	for n+base64.StdEncoding.EncodedLen(n) > limit {
		n--
	}
	for n+1+base64.StdEncoding.EncodedLen(n+1) <= limit {
		n++
	}
	data := bytes.Repeat([]byte{0xff}, n)
	part := streamMediaPart("audio/pcm", base64.RawURLEncoding.EncodeToString(data))
	part["thoughtSignature"] = strings.Repeat("s", limit-n-base64.StdEncoding.EncodedLen(n))
	parts, finished, err := decodeLiveFixture(t, liveFrame("IN_PROGRESS", part), liveFrame("SUCCEEDED", part))
	if err != nil || !finished || len(parts) != 1 || parts[0].PayloadBytes() != limit || !bytes.Equal(parts[0].InlineData.Data, data) {
		t.Fatal("exact canonical budget or deduplicated media accounting failed", len(parts), finished, err)
	}
	parts, finished, err = decodeLiveFixture(t, liveFrame("IN_PROGRESS", map[string]any{"text": "x"}), liveFrame("SUCCEEDED", part))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" || finished || len(parts) != 1 || parts[0].Text != "x" {
		t.Fatal("media plus signature bypassed cumulative budget", len(parts), finished, err)
	}
	// Unpadded input is still charged at the canonical padded output size.
	oversized := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0}, n+1))
	if _, err := decodeStreamBase64(oversized, streamInvalidInlineEncoding); !errors.As(err, &failure) || failure.Status != "RESOURCE_EXHAUSTED" {
		t.Fatal("oversized media was not rejected before decoding", err)
	}
}

func TestStreamInlineCumulativePrefixRequiresExactPartIdentity(t *testing.T) {
	prior := contract.Part{InlineData: &contract.InlineData{MIMEType: "audio/L16;rate=24000", Data: []byte{0xfb, 0xff}}, Thought: true, ThoughtSignature: "original"}
	first := phase6WireParts(t, []contract.Part{prior})
	for name, mutate := range map[string]func(map[string]any){
		"same-bytes-alternative-base64": func(p map[string]any) { p["inlineData"].(map[string]any)["data"] = "-_8" },
		"different-mime-spelling":       func(p map[string]any) { p["inlineData"].(map[string]any)["mimeType"] = "audio/l16;rate=24000" },
		"different-bytes":               func(p map[string]any) { p["inlineData"].(map[string]any)["data"] = "AA==" },
		"different-signature":           func(p map[string]any) { p["thoughtSignature"] = "changed" },
		"different-thought":             func(p map[string]any) { p["thought"] = false },
	} {
		t.Run(name, func(t *testing.T) {
			last := phase6WireParts(t, []contract.Part{prior})
			mutate(last[0])
			last = append(last, map[string]any{"text": "done"})
			parts, finished, err := decodeLiveFixture(t, liveFrame("IN_PROGRESS", first...), liveFrame("SUCCEEDED", last...))
			want := 3
			if name == "same-bytes-alternative-base64" {
				want = 2
			}
			if err != nil || !finished || len(parts) != want || !reflect.DeepEqual(parts[0], prior) || parts[len(parts)-1].Text != "done" {
				t.Fatal("media prefix guessed across bytes, MIME or annotation differences", len(parts), finished, err)
			}
		})
	}
	// Two audio fragments and one concatenated blob are different Part
	// sequences. Even identical concatenated bytes do not prove a prefix.
	parts, finished, err := decodeLiveFixture(t,
		liveFrame("IN_PROGRESS", streamMediaPart("audio/pcm", "AA==")),
		liveFrame("IN_PROGRESS", streamMediaPart("audio/pcm", "AQ==")),
		liveFrame("SUCCEEDED", streamMediaPart("audio/pcm", "AAE=")),
	)
	if err != nil || !finished || len(parts) != 3 || !bytes.Equal(parts[0].InlineData.Data, []byte{0}) || !bytes.Equal(parts[1].InlineData.Data, []byte{1}) || !bytes.Equal(parts[2].InlineData.Data, []byte{0, 1}) {
		t.Fatal("media fragments were joined or silently removed", len(parts), finished, err)
	}
}

func TestStreamInlineTruncationNeverCertifiesSuccess(t *testing.T) {
	first, err := json.Marshal(liveFrame("IN_PROGRESS", streamMediaPart("audio/pcm", "AA==")))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := json.Marshal(liveFrame("SUCCEEDED"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		"]", "," + string(terminal), "," + string(terminal) + "]{}", "," + string(terminal) + "," + string(first) + "]",
	} {
		parts, finished := 0, false
		err := decodeStream(&oneByteReader{data: []byte("[" + string(first) + suffix)}, func(event contract.Event) error {
			parts += len(event.Parts)
			finished = finished || event.Finished
			return nil
		})
		if err == nil || parts != 1 || finished {
			t.Fatal("media output bypassed terminal, complete JSON or EOF checks", parts, finished, err)
		}
	}
}
