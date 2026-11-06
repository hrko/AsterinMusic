package logging

import (
	"context"
	"log/slog"
	"sync"
)

const asyncCapacity = 512
const asyncOverflowCapacity = 32

type queuedLog struct {
	ctx     context.Context
	record  slog.Record
	handler slog.Handler
	count   int
}

// AsyncOutput owns one writer worker. A stalled writer cannot create more
// workers or block protocol/command processing. Overflow retains the latest
// record per event and reports how many records were coalesced.
type AsyncOutput struct {
	mu       sync.Mutex
	queue    []queuedLog
	overflow map[string]queuedLog
	wake     chan struct{}
	done     chan struct{}
	closed   bool
}

func Async(logger *slog.Logger) (*slog.Logger, *AsyncOutput) {
	o := &AsyncOutput{wake: make(chan struct{}, 1), done: make(chan struct{}), overflow: make(map[string]queuedLog)}
	go o.run()
	return slog.New(&asyncHandler{output: o, target: OrDiscard(logger).Handler()}), o
}

type asyncHandler struct {
	output *AsyncOutput
	target slog.Handler
}

func (h *asyncHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.target.Enabled(ctx, level)
}
func (h *asyncHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncHandler{h.output, h.target.WithAttrs(attrs)}
}
func (h *asyncHandler) WithGroup(name string) slog.Handler {
	return &asyncHandler{h.output, h.target.WithGroup(name)}
}
func (h *asyncHandler) Handle(ctx context.Context, record slog.Record) error {
	o := h.output
	item := queuedLog{ctx: ctx, record: record.Clone(), handler: h.target, count: 1}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	if len(o.queue) < asyncCapacity {
		o.queue = append(o.queue, item)
	} else {
		key := "unclassified"
		record.Attrs(func(a slog.Attr) bool {
			if a.Key == "event" && a.Value.Kind() == slog.KindString {
				key = a.Value.String()
				return false
			}
			return true
		})
		if _, ok := o.overflow[key]; !ok && len(o.overflow) >= asyncOverflowCapacity {
			key = "overflow"
		}
		if old, ok := o.overflow[key]; ok {
			item.count += old.count
		}
		o.overflow[key] = item
	}
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

func (o *AsyncOutput) run() {
	defer close(o.done)
	for {
		o.mu.Lock()
		var item queuedLog
		found := false
		if len(o.queue) > 0 {
			item, found = o.queue[0], true
			o.queue[0] = queuedLog{}
			o.queue = o.queue[1:]
		} else {
			for key, value := range o.overflow {
				item, found = value, true
				delete(o.overflow, key)
				break
			}
		}
		closed := o.closed
		o.mu.Unlock()
		if found {
			if item.count > 1 {
				item.record.AddAttrs(slog.Int("output_coalesced", item.count-1))
			}
			_ = item.handler.Handle(item.ctx, item.record)
			continue
		}
		if closed {
			return
		}
		<-o.wake
	}
}

// Close stops accepting records and waits for accepted records within ctx.
// A timed-out writer remains the same single worker until it returns.
func (o *AsyncOutput) Close(ctx context.Context) error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
