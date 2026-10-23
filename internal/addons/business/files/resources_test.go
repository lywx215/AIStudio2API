package files

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/contract"
	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/resources"
)

func TestSnapshotBudgetAndCumulativeLimitPrecedeDiskRead(t *testing.T) {
	s, _ := newTestStore(t)
	_, record := completeFile(t, s, "owner", "hello")
	if err := os.Remove(s.blob(record.ID)); err != nil {
		t.Fatal(err)
	}
	b := resources.New(0)
	ctx, scope := b.Begin(context.Background())
	defer scope.Close()
	if _, err := s.Snapshot(ctx, "owner", record.ID); !errors.Is(err, resources.ErrExhausted) {
		t.Fatal("disk opened before budget check", err)
	}
	b.SetLimit(100)
	_, err := s.Snapshot(resources.WithAttachmentAllowance(ctx, 4), "owner", record.ID)
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Code != 413 {
		t.Fatal("disk opened before cumulative check", err)
	}
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("rejected snapshot leaked")
	}
	if _, err = s.Snapshot(ctx, "owner", record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("read failure leaked")
	}
}

func TestSnapshotBufferRemainsChargedUntilScopeClose(t *testing.T) {
	s, _ := newTestStore(t)
	_, record := completeFile(t, s, "owner", "hello")
	b := resources.New(5)
	ctx, scope := b.Begin(context.Background())
	got, err := s.Snapshot(ctx, "owner", record.ID)
	if err != nil || string(got.Data) != "hello" || b.Snapshot().InUseBytes != 5 {
		t.Fatal(err, b.Snapshot())
	}
	if _, err = s.Snapshot(ctx, "owner", record.ID); !errors.Is(err, resources.ErrExhausted) {
		t.Fatal(err)
	}
	scope.Close()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("scope leaked")
	}
}

type noReadOnBudget struct{ reads int }

func (r *noReadOnBudget) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("unexpected read")
}

func TestUploadBudgetRejectsUnknownLengthBeforeRead(t *testing.T) {
	s, _ := newTestStore(t)
	token, err := s.Begin(context.Background(), "owner", "file.txt", "text/plain", 5)
	if err != nil {
		t.Fatal(err)
	}
	b := resources.New(0)
	ctx, scope := b.Begin(context.Background())
	defer scope.Close()
	spy := &noReadOnBudget{}
	if _, err = s.Chunk(ctx, token, "owner", 0, spy, true); !errors.Is(err, resources.ErrExhausted) || spy.reads != 0 {
		t.Fatal(err, spy.reads)
	}
	state, err := s.Query(context.Background(), token, "owner")
	if err != nil || state.Received != 0 {
		t.Fatal(state, err)
	}
	b.SetLimit(100)
	if _, err = s.Chunk(ctx, token, "owner", 0, strings.NewReader("hello"), true); err != nil {
		t.Fatal(err)
	}
}
