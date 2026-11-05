package discordbot

import (
	"context"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sync"
	"time"
)

// restoreTrace is bounded by the first restored track's preparation, not its
// playback duration. The playback observer ends it outside the frame path.
type restoreTrace struct {
	mu                 sync.Mutex
	ctx                context.Context
	span               trace.Span
	preparation        trace.Span
	preparationContext context.Context
	tracer             trace.Tracer
	ended              bool
}

func (s *session) beginRestore(v *playbackCheckpoint) {
	_, span := tracerOrNoop(s.app.tracer).Start(s.app.ctx, "playback.restore", trace.WithAttributes(attribute.String("asterinmusic.guild_id", s.guild.String()), attribute.String("asterinmusic.bot_id", s.app.config.ApplicationID.String()), attribute.Bool("asterinmusic.playing", v.Playing)))
	// Retain only correlation data in the immutable logging context. Callers
	// keep their own cancellation and deadlines through restoreContext.
	ctx := trace.ContextWithSpanContext(context.Background(), span.SpanContext())
	s.restore = &restoreTrace{ctx: ctx, span: span, tracer: tracerOrNoop(s.app.tracer)}
}

func (r *restoreTrace) prepare() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended || r.preparation != nil {
		return
	}
	_, r.preparation = r.tracer.Start(r.ctx, "playback.restore_prepare")
	r.preparationContext = trace.ContextWithSpanContext(context.Background(), r.preparation.SpanContext())
}
func (r *restoreTrace) finish(reason string, err error) {
	r.finishAt(reason, err, time.Now())
}

func (r *restoreTrace) audioStarted(id, channel string, at time.Time) {
	r.finishAt("audio_started", nil, at, attribute.String("asterinmusic.playback_id", id), attribute.String("asterinmusic.channel_id", channel))
}

func (r *restoreTrace) finishAt(reason string, err error, at time.Time, attrs ...attribute.KeyValue) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true
	r.span.SetAttributes(attribute.String("asterinmusic.result", reason))
	r.span.SetAttributes(attrs...)
	if r.preparation != nil {
		finishTrace(r.preparation, err, trace.WithTimestamp(at))
		r.preparation = nil
	}
	finishTrace(r.span, err, trace.WithTimestamp(at))
	r.span = nil
	r.preparationContext = nil
}
func (s *session) restoreContext(ctx context.Context) context.Context {
	if s.restore != nil {
		s.restore.mu.Lock()
		defer s.restore.mu.Unlock()
		if s.restore.ended {
			return ctx
		}
		parent := s.restore.ctx
		if s.restore.preparationContext != nil {
			parent = s.restore.preparationContext
		}
		return trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(parent))
	}
	return ctx
}
