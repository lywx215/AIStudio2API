package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func TestAttachmentBodyStreamsExactJSONWithBoundedBuffer(t *testing.T) {
	for _, size := range []int{1, 2, 3, 12287, 12288, 12289, 50001} {
		data := bytes.Repeat([]byte{0xfb}, size)
		item := contract.Attachment{Filename: "quote\".txt", MIMEType: "text/plain", Data: data}
		body, length, err := attachmentBody(context.Background(), "config\"", "session\\name", item)
		if err != nil {
			t.Fatal(err)
		}
		// Repeated tiny reads exercise pending encoded bytes and chunk boundaries.
		var raw bytes.Buffer
		buffer := make([]byte, 7)
		for {
			n, err := body.Read(buffer)
			raw.Write(buffer[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if int64(raw.Len()) != length {
			t.Fatalf("length=%d actual=%d", length, raw.Len())
		}
		var decoded struct {
			Config string                                                  `json:"configId"`
			File   struct{ Name, FileName, MIMEType, FileContents string } `json:"addContextFileRequest"`
		}
		if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		got, err := base64.StdEncoding.DecodeString(decoded.File.FileContents)
		if err != nil || !bytes.Equal(got, data) || decoded.Config != "config\"" || decoded.File.Name != "session\\name" || decoded.File.FileName != item.Filename {
			t.Fatalf("changed streamed envelope: %v", err)
		}
	}
}

func TestAttachmentBase64CancellationHasNoProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &attachmentBase64Reader{ctx: ctx, data: bytes.Repeat([]byte("x"), 50000)}
	if n, err := r.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	cancel()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAttachmentUploadBudgetRejectsBeforeTransportAndReleases(t *testing.T) {
	for _, enough := range []bool{false, true} {
		limit := resources.AttachmentUploadReserveBytes - 1
		if enough {
			limit++
		}
		budget := resources.New(limit)
		ctx, scope := budget.Begin(context.Background())
		calls := 0
		e := New(context.Background(), nil, 1, time.Second, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil || int64(len(body)) != r.ContentLength {
				t.Fatal("stream length mismatch", err)
			}
			if budget.Snapshot().InUseBytes != resources.AttachmentUploadReserveBytes {
				t.Fatal("scratch not reserved during upload")
			}
			return reply(200, `{"addContextFileResponse":{"fileId":"fixture-id"}}`), nil
		}))
		a := &lease{account: account(), token: "synthetic"}
		id, err := e.uploadAttachment(ctx, a, "session", attachment())
		if enough {
			if err != nil || id != "fixture-id" || calls != 1 {
				t.Fatal(id, calls, err)
			}
		} else if !errors.Is(err, resources.ErrExhausted) || calls != 0 {
			t.Fatal(calls, err)
		}
		if budget.Snapshot().InUseBytes != 0 {
			t.Fatal("upload scratch leaked")
		}
		scope.Close()
		e.Close()
	}
}
