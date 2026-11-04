package history

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriterBoundedAndFlush(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	w := NewWriter(func(ctx context.Context, r Record) error {
		if writes.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, nil)
	t.Cleanup(func() { w.Shutdown(context.Background()) })
	if !w.Enqueue(record(1)) {
		t.Fatal("enqueue failed")
	}
	<-entered
	for i := 0; i < PendingLimit; i++ {
		if !w.Enqueue(record(i + 2)) {
			t.Fatal("queue filled early")
		}
	}
	if w.Enqueue(record(999)) {
		t.Fatal("unbounded queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := w.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != PendingLimit+1 {
		t.Fatal(writes.Load())
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Enqueue(record(1000)) {
		t.Fatal("accepted after shutdown")
	}
}

func TestWriterRetriesAndReports(t *testing.T) {
	var attempts, reports atomic.Int32
	w := NewWriter(func(context.Context, Record) error { attempts.Add(1); return errors.New("disk failed") }, func(error) { reports.Add(1) })
	w.Enqueue(record(1))
	if err := w.Flush(context.Background()); err == nil {
		t.Fatal("failure hidden")
	}
	if attempts.Load() != 3 || reports.Load() != 1 {
		t.Fatal(attempts.Load(), reports.Load())
	}
	w.Shutdown(context.Background())
}
