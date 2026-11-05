package discordbot

import (
	"context"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sync"
)

// restoreTrace is bounded by the first restored track's preparation, not its
// playback duration. The playback observer ends it outside the frame path.
type restoreTrace struct {
	mu          sync.Mutex
	ctx         context.Context
	span        trace.Span
	preparation trace.Span
	tracer      trace.Tracer
	ended       bool
}

func (s *session) beginRestore(v *playbackCheckpoint) {
	ctx, span := tracerOrNoop(s.app.tracer).Start(s.app.ctx, "playback.restore", trace.WithAttributes(attribute.String("asterinmusic.guild_id", s.guild.String()), attribute.String("asterinmusic.bot_id", s.app.config.ApplicationID.String()), attribute.Bool("asterinmusic.playing", v.Playing)))
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
	r.ctx, r.preparation = r.tracer.Start(r.ctx, "playback.restore_prepare")
}
func (r *restoreTrace) finish(reason string, err error) {
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
	if r.preparation != nil {
		finishTrace(r.preparation, err)
	}
	finishTrace(r.span, err)
}
func (s *session) restoreContext(ctx context.Context) context.Context {
	if s.restore != nil {
		s.restore.mu.Lock()
		defer s.restore.mu.Unlock()
		if s.restore.ended {
			return ctx
		}
		return trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(s.restore.ctx))
	}
	return ctx
}
