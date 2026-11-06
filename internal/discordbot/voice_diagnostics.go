package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"log/slog"
	"time"
)

const voiceDiagnosticCapacity = 64
const voiceDiagnosticOverflowCapacity = 32

type voiceDiagnosticSinkKey struct{}
type voiceDiagnosticSink func(context.Context, slog.Level, string, ...any)

// Transport workers capture into their controller's bounded buffer. They must
// not wait for a log writer before processing the next protocol event.
func (c *voiceConnection) diagnosticContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, voiceDiagnosticSinkKey{}, voiceDiagnosticSink(func(ctx context.Context, level slog.Level, message string, attrs ...any) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.captureDiagnosticLocked(ctx, level, message, attrs...)
	}))
}

func recordVoiceDiagnostic(ctx context.Context, logger *slog.Logger, level slog.Level, message string, attrs ...any) {
	if sink, ok := ctx.Value(voiceDiagnosticSinkKey{}).(voiceDiagnosticSink); ok {
		sink(ctx, level, message, attrs...)
		return
	}
	logging.OrDiscard(logger).Log(ctx, level, message, attrs...)
}

type voiceDiagnostic struct {
	ctx     context.Context
	level   slog.Level
	message string
	attrs   []any
	at      time.Time
	first   time.Time
	count   int
}

// State changes only capture immutable values. Normal callbacks drain after
// unlocking; audio-path transitions are drained by the existing session tick.
// Under a stalled observer, overflow is coalesced by the finite event names
// used below, preserving each outcome's latest attributes and occurrence count.
func (c *voiceConnection) recordDiagnosticLocked(level slog.Level, message string, attrs ...any) {
	c.captureDiagnosticLocked(c.traceContextLocked(), level, message, attrs...)
}

func (c *voiceConnection) captureDiagnosticLocked(ctx context.Context, level slog.Level, message string, attrs ...any) {
	d := voiceDiagnostic{ctx: ctx, level: level, message: message, attrs: append([]any(nil), attrs...), at: c.now(), count: 1}
	d.first = d.at
	if len(c.diagnostics) < voiceDiagnosticCapacity {
		c.diagnostics = append(c.diagnostics, d)
		return
	}
	if c.diagnosticOverflow == nil {
		c.diagnosticOverflow = make(map[string]voiceDiagnostic)
	}
	event := diagnosticEvent(attrs)
	if _, exists := c.diagnosticOverflow[event]; !exists && len(c.diagnosticOverflow) >= voiceDiagnosticOverflowCapacity {
		event = "voice.diagnostics.overflow"
		d.message = "voice diagnostic event categories exceeded capacity"
		d.attrs = []any{"event", event}
	}
	if old, ok := c.diagnosticOverflow[event]; ok {
		d.count += old.count
		d.first = old.first
	}
	c.diagnosticOverflow[event] = d
}

func diagnosticEvent(attrs []any) string {
	for i := 0; i < len(attrs); i++ {
		switch attr := attrs[i].(type) {
		case slog.Attr:
			if attr.Key == "event" && attr.Value.Kind() == slog.KindString {
				return attr.Value.String()
			}
		case string:
			if i+1 < len(attrs) {
				i++
				if value, ok := attrs[i].(string); attr == "event" && ok {
					return value
				}
			}
		}
	}
	return "voice.diagnostics.unclassified"
}

func (c *voiceConnection) flushDiagnostics() {
	c.diagnosticMu.Lock()
	defer c.diagnosticMu.Unlock()
	c.mu.Lock()
	pending, overflow := c.diagnostics, c.diagnosticOverflow
	c.diagnostics = nil
	c.diagnosticOverflow = nil
	c.mu.Unlock()
	emit := func(d voiceDiagnostic) {
		attrs := append(d.attrs, "observed_at", d.at)
		if d.count > 1 {
			attrs = append(attrs, "occurrences", d.count, "suppressed", d.count-1, "coalesced", true, "aggregation_duration_ms", d.at.Sub(d.first).Milliseconds())
		}
		logging.OrDiscard(c.logger).Log(d.ctx, d.level, d.message, attrs...)
	}
	for _, d := range pending {
		emit(d)
	}
	for _, d := range overflow {
		emit(d)
	}
}
