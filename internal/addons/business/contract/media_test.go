package contract

import "testing"

func TestOutputMediaMIMESubset(t *testing.T) {
	for _, value := range []string{SuggestionsMIMEType, "image/png", "image/jpeg", "image/webp", "image/gif", "image/heic", "image/heif", "audio/wav", "audio/x-wav", "audio/mp3", "audio/mpeg", "audio/mp4", "audio/aac", "audio/ogg", "audio/flac", "audio/webm", "video/mp4", "video/webm", "video/mpeg", "video/quicktime", "application/pdf", "audio/pcm;rate=24000", "audio/L16; codec=pcm; rate=24000; channels=1"} {
		if !ValidOutputInlineMIME(value) {
			t.Fatalf("valid pass-through MIME rejected: %q", value)
		}
	}
	for _, value := range []string{"", "text/html", "image/svg+xml", "application/octet-stream", "image/png;foo=bar", " image/png", "image/png\n", "image/π", "audio/pcm;rate=0", "audio/pcm;rate=192001", "audio/pcm;rate=+8000", "audio/pcm;channels=9", "audio/pcm;codec=mp3", "audio/pcm;unknown=x", "audio/pcm;rate=24000;rate=16000"} {
		if ValidOutputInlineMIME(value) {
			t.Fatalf("unsupported or malformed MIME accepted: %q", value)
		}
	}
	if ValidOutputInlineData(nil) || ValidOutputInlineData(&InlineData{MIMEType: "image/png"}) || ValidOutputInlineData(&InlineData{MIMEType: SuggestionsMIMEType, Data: []byte{255}}) {
		t.Fatal("empty data or invalid suggestions accepted")
	}
	if !ValidOutputInlineData(&InlineData{MIMEType: "image/png", Data: []byte{255, 0, 254}}) {
		t.Fatal("opaque binary was incorrectly required to be UTF-8")
	}
}
