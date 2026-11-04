package history

import (
	"context"
	"errors"
	"sync"
	"time"
)

const PendingLimit = 128

type writeRequest struct {
	record  Record
	barrier chan error
}

// Writer owns a bounded queue. Enqueue never waits for SQLite; only lifecycle
// Flush and Shutdown wait. Records must have a stable occurrence ID for retry.
type Writer struct {
	mu     sync.Mutex
	closed bool
	queue  chan writeRequest
	done   chan struct{}
	cancel context.CancelFunc
}

func NewWriter(appendRecord func(context.Context, Record) error, report func(error)) *Writer {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Writer{queue: make(chan writeRequest, PendingLimit), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(w.done)
		var failure error
		for {
			select {
			case <-ctx.Done():
				return
			case request, ok := <-w.queue:
				if !ok {
					return
				}
				if request.barrier != nil {
					request.barrier <- failure
					failure = nil
					continue
				}
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					writeCtx, c := context.WithTimeout(ctx, 2*time.Second)
					err = appendRecord(writeCtx, request.record)
					c()
					if err == nil || ctx.Err() != nil {
						break
					}
					if attempt < 2 {
						timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
						select {
						case <-timer.C:
						case <-ctx.Done():
							timer.Stop()
						}
					}
				}
				if err != nil {
					failure = errors.New("history write failed")
					if report != nil {
						report(err)
					}
				}
			}
		}
	}()
	return w
}

func (w *Writer) Enqueue(r Record) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	if d := r.Entry.Track.Duration; d != nil {
		value := *d
		r.Entry.Track.Duration = &value
	}
	// Import metadata is not needed to replay a single track.
	r.Entry.PlaylistURL = ""
	r.Entry.ImportID = ""
	select {
	case w.queue <- writeRequest{record: r}:
		return true
	default:
		return false
	}
}

func (w *Writer) Flush(ctx context.Context) error {
	barrier := make(chan error, 1)
	// Never wait for space while holding mu: Enqueue is on the audio path.
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return errors.New("history writer closed")
		}
		select {
		case w.queue <- writeRequest{barrier: barrier}:
			w.mu.Unlock()
			select {
			case err := <-barrier:
				return err
			case <-ctx.Done():
				return ctx.Err()
			case <-w.done:
				return errors.New("history writer stopped")
			}
		default:
			w.mu.Unlock()
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (w *Writer) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		w.cancel()
		return nil
	case <-ctx.Done():
		w.cancel()
		return ctx.Err()
	}
}
