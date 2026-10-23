package resources

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestBudgetConcurrentReservationsNeverOversubscribe(t *testing.T) {
	b := New(10)
	start := make(chan struct{})
	releaseAll := make(chan struct{})
	results := make(chan bool, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, l := b.Begin(context.Background())
			defer l.Close()
			<-start
			release, err := Reserve(ctx, 3)
			results <- err == nil
			if err == nil {
				<-releaseAll
				release()
				release()
			}
		}()
	}
	close(start)
	successes := 0
	for range 32 {
		if <-results {
			successes++
		}
	}
	if successes != 3 || b.Snapshot().InUseBytes != 9 {
		t.Fatal(successes, b.Snapshot())
	}
	close(releaseAll)
	wg.Wait()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}

func TestBudgetLowerLimitKeepsExistingLeaseAndCloseIsIdempotent(t *testing.T) {
	b := New(100)
	_, l := b.Begin(context.Background())
	release, err := l.Reserve(80)
	if err != nil {
		t.Fatal(err)
	}
	b.SetLimit(20)
	if got := b.Snapshot(); got.InUseBytes != 80 || got.AvailableBytes != 0 {
		t.Fatal(got)
	}
	if _, err := l.Reserve(1); !errors.Is(err, ErrExhausted) {
		t.Fatal(err)
	}
	l.Close()
	l.Close()
	release()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal(b.Snapshot())
	}
	if _, err := l.Reserve(1); !errors.Is(err, ErrLeaseClosed) {
		t.Fatal(err)
	}
}

func TestBudgetCancellationDoesNotPrematurelyReturnLiveBuffers(t *testing.T) {
	b := New(10)
	parent, cancel := context.WithCancel(context.Background())
	_, l := b.Begin(parent)
	release, err := l.Reserve(8)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := l.Reserve(1); err == nil {
		t.Fatal("cancelled lease allocated")
	}
	if b.Snapshot().InUseBytes != 8 {
		t.Fatal("live reservation released at cancellation")
	}
	release()
	l.Close()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}

type spyReader struct{ reads int }

func (r *spyReader) Read([]byte) (int, error) { r.reads++; return 0, io.EOF }
func TestReadAllRejectsFullBudgetBeforeReadAndReleasesFailures(t *testing.T) {
	b := New(0)
	ctx, l := b.Begin(context.Background())
	defer l.Close()
	reader := &spyReader{}
	if _, err := ReadAll(ctx, reader, 100); !errors.Is(err, ErrExhausted) || reader.reads != 0 {
		t.Fatal(err, reader.reads)
	}
	b.SetLimit(1000)
	if _, err := ReadAll(ctx, strings.NewReader(strings.Repeat("x", 101)), 100); err == nil {
		t.Fatal("body limit bypassed")
	}
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal("failed body leaked", b.Snapshot())
	}
}

func TestReadAllUnknownLengthIsMeteredAndReturnedBufferRemainsCharged(t *testing.T) {
	b := New(1000)
	ctx, l := b.Begin(context.Background())
	data, err := ReadAll(ctx, strings.NewReader("payload"), 100)
	if err != nil || string(data) != "payload" {
		t.Fatal(string(data), err)
	}
	if b.Snapshot().InUseBytes != 7 {
		t.Fatal(b.Snapshot())
	}
	l.Close()
	if b.Snapshot().InUseBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}
