package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const RepetitionInterval = time.Minute
const RepetitionCapacity = 256

type repeated struct {
	first, last time.Time
	suppressed  int
	attrs       []any
}

// Repetition aggregates repeated failures within one owner's lifetime. Keys
// include the affected target and reason. No goroutines or unbounded queues
// are used; eviction emits a summary before forgetting the target.
type Repetition struct {
	mu      sync.Mutex
	entries map[string]repeated
	now     func() time.Time
}

func (r *Repetition) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func summary(ctx context.Context, l *slog.Logger, e repeated, now time.Time, reason string) {
	if e.suppressed == 0 {
		return
	}
	attrs := append([]any(nil), e.attrs...)
	attrs = append(attrs, "event", "logging.repetition.summary", "suppressed", e.suppressed, "duration_ms", now.Sub(e.first).Milliseconds(), "summary_reason", reason)
	OrDiscard(l).WarnContext(ctx, "repeated failures summarized", attrs...)
}

func (r *Repetition) Failure(ctx context.Context, l *slog.Logger, key, message string, attrs ...any) {
	now := r.clock()
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]repeated)
	}
	e, found := r.entries[key]
	if found && now.Sub(e.last) < RepetitionInterval {
		e.suppressed++
		r.entries[key] = e
		r.mu.Unlock()
		return
	}
	var evicted *repeated
	if !found && len(r.entries) >= RepetitionCapacity {
		var oldest string
		var when time.Time
		for k, v := range r.entries {
			if oldest == "" || v.last.Before(when) {
				oldest, when = k, v.last
			}
		}
		v := r.entries[oldest]
		evicted = &v
		delete(r.entries, oldest)
	}
	first := now
	if found {
		first = e.first
	}
	r.entries[key] = repeated{first: first, last: now, attrs: append([]any(nil), attrs...)}
	r.mu.Unlock()
	if evicted != nil {
		summary(ctx, l, *evicted, now, "capacity")
	}
	if found {
		summary(ctx, l, e, now, "interval")
	}
	OrDiscard(l).WarnContext(ctx, message, attrs...)
}

// Resolve releases state and always records recovery for an observed failure.
func (r *Repetition) Resolve(ctx context.Context, l *slog.Logger, key string) {
	now := r.clock()
	r.mu.Lock()
	e, found := r.entries[key]
	delete(r.entries, key)
	r.mu.Unlock()
	if !found {
		return
	}
	summary(ctx, l, e, now, "recovered")
	attrs := append([]any(nil), e.attrs...)
	attrs = append(attrs, "event", "logging.repetition.recovered", "duration_ms", now.Sub(e.first).Milliseconds())
	OrDiscard(l).InfoContext(ctx, "operation recovered", attrs...)
}

// Close flushes remaining summaries when the owner terminates.
func (r *Repetition) Close(ctx context.Context, l *slog.Logger) {
	now := r.clock()
	r.mu.Lock()
	entries := r.entries
	r.entries = nil
	r.mu.Unlock()
	for _, e := range entries {
		summary(ctx, l, e, now, "closed")
	}
}

// ResolveAll is for owners with a single operation and several failure reasons.
// Call only after an actual successful attempt, not a skipped polling cycle.
func (r *Repetition) ResolveAll(ctx context.Context, l *slog.Logger) {
	r.mu.Lock()
	keys := make([]string, 0, len(r.entries))
	for key := range r.entries {
		keys = append(keys, key)
	}
	r.mu.Unlock()
	for _, key := range keys {
		r.Resolve(ctx, l, key)
	}
}
