package discordbot

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"sync"
	"time"
)

func tracerOrNoop(t trace.Tracer) trace.Tracer {
	if t == nil {
		return noop.NewTracerProvider().Tracer("asterinmusic/discordbot")
	}
	return t
}
func finishTrace(span trace.Span, err error, opts ...trace.SpanEndOption) {
	if err != nil {
		reason := failureClass(err)
		span.SetAttributes(attribute.String("asterinmusic.reason", reason))
		// Reuse the log allowlist, copying typed numeric diagnostics only.
		attrs := failureAttributes(err)
		for i := 0; i+1 < len(attrs); i += 2 {
			key, named := attrs[i].(string)
			value, numeric := attrs[i+1].(int)
			if named && numeric {
				span.SetAttributes(attribute.Int("asterinmusic."+key, value))
			}
		}
		if !errors.Is(err, context.Canceled) {
			span.SetStatus(codes.Error, "")
			span.SetAttributes(attribute.String("error.type", reason))
		}
	}
	span.End(opts...)
}

// voiceTrace belongs to a bounded recovery, including notification-only VC
// moves which do not have a supervisor. All fields are protected by c.mu.
type voiceTrace struct {
	ctx           context.Context
	span          trace.Span
	notifications trace.Span
	attempts      int
}

func (c *voiceConnection) startTraceLocked(cause context.Context, trigger string) {
	if c.observation != nil {
		return
	}
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attribute.String("asterinmusic.guild_id", c.guild.String()), attribute.String("asterinmusic.bot_id", c.user.String()), attribute.String("asterinmusic.channel_id", c.channel.String()), attribute.String("asterinmusic.trigger", trigger))}
	if sc := trace.SpanContextFromContext(cause); sc.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: sc}))
	}
	ctx, span := tracerOrNoop(c.tracer).Start(context.Background(), "voice.recovery", opts...)
	c.observation = &voiceTrace{ctx: ctx, span: span}
}

// Notification collection is distinct from waiting for a ready transport.
// It belongs to the bounded recovery, survives notification-driven generation
// changes, and ends before the handshake begins. All access holds c.mu.
func (c *voiceConnection) startNotificationTraceLocked() {
	o := c.observation
	if o == nil || o.notifications != nil {
		return
	}
	_, o.notifications = tracerOrNoop(c.tracer).Start(o.ctx, "voice.notifications", trace.WithAttributes(attribute.Int64("asterinmusic.generation_start", int64(c.generation))))
}

func (c *voiceConnection) endNotificationTraceLocked(err error) {
	o := c.observation
	if o == nil || o.notifications == nil {
		return
	}
	o.notifications.SetAttributes(attribute.Bool("asterinmusic.membership_received", c.state != nil), attribute.Bool("asterinmusic.server_received", c.server != nil), attribute.Int64("asterinmusic.generation", int64(c.generation)))
	finishTrace(o.notifications, err)
	o.notifications = nil
}
func (c *voiceConnection) traceContextLocked() context.Context {
	if c.observation != nil {
		return c.observation.ctx
	}
	return context.Background()
}
func (c *voiceConnection) endTraceLocked(phase voicePhase) {
	if c.observation == nil {
		return
	}
	s := c.observation.span
	if c.observation.notifications != nil {
		err := error(context.Canceled)
		if c.failureReason == "timeout" {
			err = context.DeadlineExceeded
		}
		c.endNotificationTraceLocked(err)
	}
	s.SetAttributes(attribute.String("asterinmusic.result", string(phase)), attribute.String("asterinmusic.channel_id", c.channel.String()), attribute.Int64("asterinmusic.generation", int64(c.generation)))
	if phase == voiceFailed && !c.removed {
		s.SetAttributes(attribute.String("asterinmusic.stage", c.failureStage), attribute.String("asterinmusic.reason", c.failureReason), attribute.Bool("asterinmusic.queue_retained", true))
		if c.failureReason != "canceled" {
			s.SetStatus(codes.Error, "")
			s.SetAttributes(attribute.String("error.type", c.failureReason))
		}
		if c.failureReason == "timeout" {
			s.SetAttributes(attribute.String("asterinmusic.deadline_scope", "recovery"))
		}
		if c.failureCode != 0 {
			s.SetAttributes(attribute.Int("asterinmusic.close_code", c.failureCode))
		}
	}
	s.End()
	c.observation = nil
}

// Each attempt owns progress independently of the current controller generation.
type voiceTraceAttempt struct {
	mu             sync.Mutex
	ctx            context.Context
	span           trace.Span
	tracer         trace.Tracer
	stagePrefix    string
	stage          trace.Span
	name           string
	ended          bool
	lastOpcode     int
	lastEvent      time.Time
	handling       bool
	handlingOpcode int
	stageStarted   time.Time
	lastCompleted  string
	endedAt        time.Time
	stageDeadline  time.Time
	deadlineScope  string
}
type voiceTraceKey struct{}

func traceVoiceEvent(ctx context.Context, opcode int, handling bool) {
	if a, ok := ctx.Value(voiceTraceKey{}).(*voiceTraceAttempt); ok {
		a.mu.Lock()
		if !a.ended {
			a.handlingOpcode, a.handling = opcode, handling
		}
		a.mu.Unlock()
	}
}

func traceVoiceReceived(ctx context.Context, opcode int) {
	if a, ok := ctx.Value(voiceTraceKey{}).(*voiceTraceAttempt); ok {
		a.mu.Lock()
		if !a.ended {
			a.lastOpcode, a.lastEvent = opcode, time.Now()
		}
		a.mu.Unlock()
	}
}

func (a *voiceTraceAttempt) diagnostic() []any {
	a.mu.Lock()
	defer a.mu.Unlock()
	attrs := []any{"handshake_stage", a.name, "event_handler_active", a.handling}
	if !a.stageDeadline.IsZero() {
		attrs = append(attrs, "stage_deadline", a.stageDeadline, "deadline_scope", a.deadlineScope)
	}
	now := time.Now()
	if !a.endedAt.IsZero() {
		now = a.endedAt
	}
	if !a.stageStarted.IsZero() {
		attrs = append(attrs, "stage_duration_ms", now.Sub(a.stageStarted).Milliseconds())
	}
	if a.lastCompleted != "" {
		attrs = append(attrs, "last_completed_stage", a.lastCompleted)
	}
	if a.handling {
		attrs = append(attrs, "handling_opcode", a.handlingOpcode)
	}
	if !a.lastEvent.IsZero() {
		attrs = append(attrs, "last_opcode", a.lastOpcode, "last_event_age_ms", now.Sub(a.lastEvent).Milliseconds())
	}
	return attrs
}

func (a *voiceTraceAttempt) progress(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progressLocked(name)
}

func (a *voiceTraceAttempt) progressLocked(name string) {
	if a.ended {
		return
	}
	if a.name == name {
		return
	}
	if a.stage != nil {
		a.stage.End()
		a.lastCompleted = a.name
	}
	a.name = name
	a.stageStarted = time.Now()
	prefix := a.stagePrefix
	if prefix == "" {
		prefix = "voice."
	}
	_, a.stage = a.tracer.Start(a.ctx, prefix+name)
}
func (a *voiceTraceAttempt) current() string { a.mu.Lock(); defer a.mu.Unlock(); return a.name }
func (a *voiceTraceAttempt) end(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended {
		return
	}
	a.ended = true
	a.endedAt = time.Now()
	a.span.SetAttributes(attribute.String("asterinmusic.stage", a.name))
	if !a.stageDeadline.IsZero() {
		a.span.SetAttributes(attribute.String("asterinmusic.deadline_scope", a.deadlineScope), attribute.String("asterinmusic.stage_deadline", a.stageDeadline.UTC().Format(time.RFC3339Nano)))
	}
	if a.stage != nil {
		finishTrace(a.stage, err)
		a.stage = nil
	}
	finishTrace(a.span, err)
	a.span = nil
	a.ctx = context.Background()
}
func traceVoiceStage(ctx context.Context, name string) {
	if a, ok := ctx.Value(voiceTraceKey{}).(*voiceTraceAttempt); ok {
		a.progress(name)
	}
}
func (c *voiceConnection) newTraceAttemptLocked(ctx context.Context) context.Context {
	t := tracerOrNoop(c.tracer)
	attempt := 1
	if c.observation != nil {
		c.observation.attempts++
		attempt = c.observation.attempts
	}
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(c.traceContextLocked()))
	ctx, span := t.Start(ctx, "voice.connect", trace.WithAttributes(attribute.Int64("asterinmusic.generation", int64(c.generation)), attribute.Int("asterinmusic.attempt", attempt), attribute.String("asterinmusic.deadline_scope", "recovery")))
	a := &voiceTraceAttempt{ctx: ctx, span: span, tracer: t, name: "establish"}
	c.traceAttempt = a
	return context.WithValue(ctx, voiceTraceKey{}, a)
}
