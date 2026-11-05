package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"log/slog"
	"time"
)

const voiceDiagnosticCapacity = 64

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
	d := voiceDiagnostic{ctx: c.traceContextLocked(), level: level, message: message, attrs: attrs, at: c.now(), count: 1}
	d.first = d.at
	if len(c.diagnostics) < voiceDiagnosticCapacity {
		c.diagnostics = append(c.diagnostics, d)
		return
	}
	if c.diagnosticOverflow == nil {
		c.diagnosticOverflow = make(map[string]voiceDiagnostic)
	}
	// Every caller uses event as its first attribute. Never key by remote input.
	event := attrs[1].(string)
	if old, ok := c.diagnosticOverflow[event]; ok {
		d.count += old.count
		d.first = old.first
	}
	c.diagnosticOverflow[event] = d
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
