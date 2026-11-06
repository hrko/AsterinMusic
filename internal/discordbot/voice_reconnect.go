package discordbot

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const voiceStageTimeout = 5 * time.Second
const voiceResumeAttempts = 2
const voiceRejoinWindow = 5 * time.Minute

var errRejoinDenied = errors.New("voice rejoin not authorized")
var errRejoinBudget = errors.New("voice rejoin budget exhausted")

type voiceRecoveryFlow struct {
	id         uint64
	ctx        context.Context
	cancel     context.CancelFunc
	stage      string
	attemptErr error
	leaving    bool
	left       bool
	explicit   bool
	// Retain the receipt of both notifications after launch consumes server.
	handshakeStarted bool
	resetMembership  bool
}

func resumeAllowed(err error) bool {
	if errors.Is(err, errDaveTransportUnsafe) {
		return false
	}
	var ws *websocket.CloseError
	if errors.As(err, &ws) {
		return ws.Code == 4015 || ws.Code == 1006 || ws.Code == 1001
	}
	return !errors.Is(err, context.Canceled)
}

// Only transport loss or an expired session may initiate a new membership
// request. Protocol errors, kicks and rate limits must not trigger re-entry.
func reconnectAllowed(err error) bool {
	if errors.Is(err, errDaveTransportUnsafe) {
		return false
	}
	var ws *websocket.CloseError
	if errors.As(err, &ws) {
		return resumeAllowed(err) || ws.Code == 4006 || ws.Code == 4009
	}
	return !errors.Is(err, context.Canceled)
}

func (c *voiceConnection) cancelRecoveryLocked() {
	if c.recovery != nil {
		c.recovery.cancel()
		c.recovery = nil
	}
}

// Stop cancels the supervisor synchronously, before session teardown is
// scheduled. No network I/O or log writer is called while the session is locked.
func (c *voiceConnection) stopRecovery() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelRecoveryLocked()
	if c.phase != voiceClosed {
		c.discardLocked()
		c.failureStage = "stop"
		c.failureReason = "canceled"
		// Close will publish the final transition; do not report explicit
		// stop as a connection failure.
		c.phase = voiceFailed
		c.endTraceLocked(voiceClosed)
		close(c.changed)
		c.changed = make(chan struct{})
	}
}

func (c *voiceConnection) recoveryValidLocked(f *voiceRecoveryFlow) bool {
	return c.recovery == f && f.ctx.Err() == nil && !c.removed && c.phase != voiceClosed && c.phase != voiceFailed
}

// One supervisor owns the recovery budget. Transport generations may change
// within it, but never grant additional time or reset the rejoin limit.
func (c *voiceConnection) startRecoveryLocked(parent context.Context, resume, sendJoin, explicit, resetMembership bool) {
	c.startTraceLocked(parent, "recovery")
	c.cancelRecoveryLocked()
	if c.deadline.IsZero() {
		c.recoveryStart = c.now()
		c.deadline = c.now().Add(voiceRecoveryTimeout)
		c.rejoins = 0
	}
	c.recoverySerial++
	c.observation.span.SetAttributes(attribute.Int64("asterinmusic.recovery_id", int64(c.recoverySerial)))
	ctx, cancel := context.WithDeadline(parent, c.deadline)
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(c.traceContextLocked()))
	f := &voiceRecoveryFlow{id: c.recoverySerial, ctx: ctx, cancel: cancel, explicit: explicit, resetMembership: resetMembership}
	c.recovery = f
	c.armLocked()
	go c.runRecovery(f, resume, sendJoin)
}

func (c *voiceConnection) recoveryStage(f *voiceRecoveryFlow, stage string, attempt int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.recoveryValidLocked(f) {
		return false
	}
	f.stage = stage
	if c.observation != nil {
		c.observation.span.AddEvent("recovery.stage", trace.WithAttributes(attribute.String("asterinmusic.stage", stage), attribute.Int("asterinmusic.attempt", attempt), attribute.Int64("asterinmusic.generation", int64(c.generation)), attribute.Int64("asterinmusic.remaining_ms", max(0, c.deadline.Sub(c.now()).Milliseconds()))))
	}
	c.recordDiagnosticLocked(slog.LevelDebug, "voice recovery attempt", "event", "voice.recovery.attempt", "recovery_id", f.id, "stage", stage, "attempt", attempt, "generation", c.generation, "remaining_ms", max(0, c.deadline.Sub(c.now()).Milliseconds()))
	return true
}

func (c *voiceConnection) recoveryFailure(f *voiceRecoveryFlow, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.recovery != f || c.phase == voiceReady || c.phase == voiceClosed || c.removed {
		return
	}
	c.failureStage, c.failureReason = f.stage, failureClass(err)
	var detail *voiceStageError
	if errors.As(err, &detail) {
		c.failureStage = detail.stage
	}
	if errors.Is(err, errRejoinDenied) {
		c.failureReason = "permission_unconfirmed"
	}
	if errors.Is(err, errRejoinBudget) {
		c.failureReason = "retry_limit"
	}
	if errors.Is(err, context.DeadlineExceeded) && (f.stage == "voice_notifications" || f.stage == "rejoin_notifications") {
		if c.traceAttempt != nil {
			c.failureStage = c.traceAttempt.current()
		} else if c.phase == voiceConnecting {
			c.failureStage = "establish"
		} else if c.state == nil {
			c.failureStage = "membership_notification"
		} else if c.server == nil {
			c.failureStage = "server_notification"
		}
	}
	c.discardLocked()
	c.signalLocked(voiceFailed)
	c.cancelRecoveryLocked()
}

func (c *voiceConnection) waitRecovery(f *voiceRecoveryFlow, duration time.Duration, leaving bool) (result error) {
	until := "ready"
	if leaving {
		until = "leave_confirmation"
	}
	return c.waitRecoveryUntil(f, duration, until)
}

func (c *voiceConnection) waitRecoveryUntil(f *voiceRecoveryFlow, duration time.Duration, until string) (result error) {
	ctx, cancel := context.WithTimeout(f.ctx, duration)
	defer cancel()
	c.mu.Lock()
	stage := f.stage
	c.mu.Unlock()
	ctx, observed := tracerOrNoop(c.tracer).Start(ctx, "voice.wait", trace.WithAttributes(attribute.String("asterinmusic.stage", stage), attribute.String("asterinmusic.wait_until", until)))
	defer func() {
		if errors.Is(result, context.DeadlineExceeded) {
			scope := "stage"
			if errors.Is(f.ctx.Err(), context.DeadlineExceeded) {
				scope = "recovery"
			}
			observed.SetAttributes(attribute.String("asterinmusic.deadline_scope", scope))
		}
		finishTrace(observed, result)
	}()
	for {
		c.mu.Lock()
		if c.phase == voiceReady && until != "leave_confirmation" {
			c.mu.Unlock()
			return nil
		}
		if !c.recoveryValidLocked(f) {
			c.mu.Unlock()
			return context.Canceled
		}
		if (until == "leave_confirmation" && f.left) || (until == "notifications" && f.handshakeStarted) {
			c.mu.Unlock()
			return nil
		}
		if f.attemptErr != nil {
			err := f.attemptErr
			c.mu.Unlock()
			return err
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Sending is serialized with Close, and validity is rechecked after taking
// sendMu. Gateway failures remain distinct from missing Discord notifications.
func (c *voiceConnection) recoveryUpdate(f *voiceRecoveryFlow, leave bool) (result error) {
	ctx, cancel := context.WithTimeout(f.ctx, voiceStageTimeout)
	defer cancel()
	ctx, observed := tracerOrNoop(c.tracer).Start(ctx, "voice.membership_request", trace.WithAttributes(attribute.Bool("asterinmusic.leave", leave)))
	defer func() { finishTrace(observed, result) }()
	for attempt := 1; ; attempt++ {
		c.sendMu.Lock()
		c.mu.Lock()
		if !c.recoveryValidLocked(f) {
			c.mu.Unlock()
			c.sendMu.Unlock()
			return context.Canceled
		}
		channel, mute, deaf := c.channel, c.mute, c.deaf
		c.mu.Unlock()
		var target *snowflake.ID
		if !leave {
			target = &channel
		}
		err := c.update(ctx, c.guild, target, mute, deaf)
		c.sendMu.Unlock()
		if err == nil {
			return nil
		}
		c.mu.Lock()
		if c.recovery == f {
			c.recordDiagnosticLocked(slog.LevelDebug, "voice gateway request failed", "event", "voice.request.failed", "recovery_id", f.id, "stage", f.stage, "reason", failureClass(err))
		}
		c.mu.Unlock()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil || attempt >= 3 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*250+rand.IntN(101)) * time.Millisecond):
		}
	}
}

func (c *voiceConnection) runRecovery(f *voiceRecoveryFlow, resume, sendJoin bool) {
	defer c.flushDiagnostics()
	defer func() {
		if f.ctx.Err() != nil {
			c.recoveryFailure(f, f.ctx.Err())
		}
	}()
	if f.resetMembership {
		joined, err := c.confirmRecoveryMembership(f)
		if err != nil {
			c.recoveryFailure(f, err)
			return
		}
		if joined {
			c.rejoinRecovery(f)
			return
		}
	}
	if resume {
		for attempt := 1; attempt <= voiceResumeAttempts; attempt++ {
			if !c.recoveryStage(f, "resume", attempt) {
				return
			}
			c.mu.Lock()
			t := c.active
			c.resuming = true
			f.attemptErr = nil
			c.mu.Unlock()
			resumable, ok := t.(interface{ Resume(context.Context) error })
			if !ok {
				break
			}
			ctx, cancel := context.WithTimeout(f.ctx, voiceStageTimeout)
			ctx, observed := tracerOrNoop(c.tracer).Start(ctx, "voice.resume", trace.WithAttributes(attribute.Int("asterinmusic.attempt", attempt)))
			progress := &voiceTraceAttempt{ctx: ctx, span: observed, tracer: tracerOrNoop(c.tracer), stagePrefix: "voice.resume.", name: "resume_start", id: cryptorand.Text()}
			progress.stageDeadline, _ = ctx.Deadline()
			progress.deadlineScope = "attempt"
			if recoveryDeadline, ok := f.ctx.Deadline(); ok && progress.stageDeadline.Equal(recoveryDeadline) {
				progress.deadlineScope = "recovery"
			}
			ctx = context.WithValue(ctx, voiceTraceKey{}, progress)
			ctx = c.diagnosticContext(ctx)
			c.mu.Lock()
			if !c.recoveryValidLocked(f) || c.active != t {
				c.mu.Unlock()
				cancel()
				progress.end(context.Canceled)
				return
			}
			c.traceAttempt = progress
			c.lastAttemptDiagnostic = nil
			generation := c.generation
			progress.generation = generation
			c.mu.Unlock()
			err := resumable.Resume(ctx)
			if errors.Is(err, context.DeadlineExceeded) {
				scope := "attempt"
				if errors.Is(f.ctx.Err(), context.DeadlineExceeded) {
					scope = "recovery"
				}
				observed.SetAttributes(attribute.String("asterinmusic.deadline_scope", scope))
			}
			progress.end(err)
			attemptContextErr := ctx.Err()
			cancel()
			c.mu.Lock()
			if c.traceAttempt == progress {
				c.lastAttemptDiagnostic = progress.diagnostic()
				c.traceAttempt = nil
			}
			c.mu.Unlock()
			if err == nil {
				// Resumed is final for the socket handshake. DAVE uses the
				// remaining recovery budget, never another Resume request.
				if waitErr := c.waitResumedDave(f, t); waitErr != nil {
					c.recoveryFailure(f, waitErr)
					return
				}
			}
			c.mu.Lock()
			if err != nil {
				code := 0
				var closed *websocket.CloseError
				if errors.As(err, &closed) {
					code = closed.Code
				}
				attrs := []any{"event", "voice.resume.failed", "recovery_id", f.id, "generation", generation, "attempt", attempt, "reason", failureClass(err), "close_code", code, "attempt_context_done", attemptContextErr != nil, "recovery_context_done", f.ctx.Err() != nil, "attempt_timed_out", errors.Is(attemptContextErr, context.DeadlineExceeded), "recovery_timed_out", errors.Is(f.ctx.Err(), context.DeadlineExceeded)}
				attrs = append(attrs, progress.diagnostic()...)
				c.captureDiagnosticLocked(ctx, slog.LevelDebug, "voice resume failed", attrs...)
			}
			if !c.recoveryValidLocked(f) {
				c.mu.Unlock()
				return
			}
			if err == nil && f.attemptErr != nil {
				terminal := f.attemptErr
				c.mu.Unlock()
				c.recoveryFailure(f, terminal)
				return
			}
			if err == nil {
				c.resuming = false
				c.deadline = time.Time{}
				if c.timer != nil {
					c.timer.Stop()
				}
				c.signalLocked(voiceReady)
				c.cancelRecoveryLocked()
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			if err != nil && !reconnectAllowed(err) {
				c.recoveryFailure(f, err)
				return
			}
			if err != nil && !resumeAllowed(err) {
				break
			}
			if attempt < voiceResumeAttempts {
				select {
				case <-f.ctx.Done():
					c.recoveryFailure(f, f.ctx.Err())
					return
				case <-time.After(time.Duration(200+rand.IntN(201)) * time.Millisecond):
				}
			}
		}
		sendJoin = true
	}
	if sendJoin {
		if !c.recoveryStage(f, "join_request", 1) {
			return
		}
		c.mu.Lock()
		if !c.recoveryValidLocked(f) {
			c.mu.Unlock()
			return
		}
		c.discardLocked()
		c.endNotificationTraceLocked(context.Canceled)
		c.state = nil
		c.server = nil
		c.lastToken = ""
		c.statePending = true
		f.attemptErr = nil
		f.handshakeStarted = false
		c.signalLocked(voiceWaiting)
		c.mu.Unlock()
		if err := c.recoveryUpdate(f, false); err != nil {
			c.recoveryFailure(f, err)
			return
		}
	}
	if !c.recoveryStage(f, "voice_notifications", 1) {
		return
	}
	err := c.waitRecoveryUntil(f, voiceStageTimeout, "notifications")
	if err == nil {
		if !c.recoveryStage(f, "establish", 1) {
			return
		}
		if err := c.waitRecovery(f, voiceRecoveryTimeout, false); err != nil {
			c.recoveryFailure(f, err)
		}
		return
	}
	if !errors.Is(err, context.DeadlineExceeded) || f.ctx.Err() != nil {
		c.recoveryFailure(f, err)
		return
	}
	c.rejoinRecovery(f)
}

func (c *voiceConnection) waitResumedDave(f *voiceRecoveryFlow, t voiceTransport) error {
	if !c.recoveryStage(f, "resume_dave", 1) {
		return context.Canceled
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		valid, same, err, changed := c.recoveryValidLocked(f), c.active == t, f.attemptErr, c.changed
		c.mu.Unlock()
		if f.ctx.Err() != nil {
			return f.ctx.Err()
		}
		if !valid || !same {
			return context.Canceled
		}
		if err != nil {
			return err
		}
		if t.Ready() {
			return nil
		}
		select {
		case <-f.ctx.Done():
			return f.ctx.Err()
		case <-changed:
		case <-ticker.C:
		}
	}
}

func (c *voiceConnection) rejoinRecovery(f *voiceRecoveryFlow) {
	if !c.recoveryStage(f, "rejoin_permission", 1) {
		return
	}
	c.mu.Lock()
	channel := c.channel
	check := c.canRejoin
	previousError := f.attemptErr
	c.mu.Unlock()
	if previousError != nil && !reconnectAllowed(previousError) {
		c.recoveryFailure(f, previousError)
		return
	}
	permissionCtx, permissionSpan := tracerOrNoop(c.tracer).Start(f.ctx, "voice.rejoin_permission")
	allowed := check != nil && check(permissionCtx, channel)
	if allowed {
		finishTrace(permissionSpan, nil)
	} else {
		finishTrace(permissionSpan, errRejoinDenied)
	}
	if !allowed {
		c.recoveryFailure(f, errRejoinDenied)
		return
	}
	if _, err := c.confirmRecoveryMembership(f); err != nil {
		c.recoveryFailure(f, err)
		return
	}
	c.mu.Lock()
	if !c.recoveryValidLocked(f) || c.channel != channel {
		c.mu.Unlock()
		return
	}
	// Notifications may arrive during the permission check. Once a handshake
	// has started, its outcome is terminal for this recovery, even on failure.
	if f.handshakeStarted {
		c.mu.Unlock()
		if c.recoveryStage(f, "establish", 1) {
			if err := c.waitRecovery(f, voiceRecoveryTimeout, false); err != nil {
				c.recoveryFailure(f, err)
			}
		}
		return
	}
	recent := c.rejoinTimes[:0]
	for _, at := range c.rejoinTimes {
		if c.now().Sub(at) < voiceRejoinWindow {
			recent = append(recent, at)
		}
	}
	c.rejoinTimes = recent
	if c.rejoins >= 1 || (!f.explicit && len(recent) >= 2) {
		c.mu.Unlock()
		c.recoveryFailure(f, errRejoinBudget)
		return
	}
	c.rejoins++
	if !f.explicit {
		c.rejoinTimes = append(c.rejoinTimes, c.now())
	}
	c.discardLocked()
	c.endNotificationTraceLocked(context.Canceled)
	joined := c.confirmedChannel != 0
	f.leaving = joined
	f.left = false
	f.attemptErr = nil
	f.stage = "leave_request"
	c.state = nil
	c.server = nil
	c.statePending = false
	c.signalLocked(voiceRecovering)
	c.mu.Unlock()
	if joined {
		if err := c.recoveryUpdate(f, true); err != nil {
			c.recoveryFailure(f, err)
			return
		}
		if !c.recoveryStage(f, "leave_confirmation", 1) {
			return
		}
		if err := c.waitRecovery(f, voiceStageTimeout, true); err != nil {
			c.recoveryFailure(f, err)
			return
		}
	}
	c.mu.Lock()
	if !c.recoveryValidLocked(f) {
		c.mu.Unlock()
		return
	}
	f.leaving = false
	f.stage = "rejoin_request"
	f.handshakeStarted = false
	c.state = nil
	c.server = nil
	c.lastToken = ""
	c.statePending = true
	c.signalLocked(voiceWaiting)
	c.mu.Unlock()
	if err := c.recoveryUpdate(f, false); err != nil {
		c.recoveryFailure(f, err)
		return
	}
	if !c.recoveryStage(f, "rejoin_notifications", 1) {
		return
	}
	if err := c.waitRecoveryUntil(f, voiceStageTimeout, "notifications"); err != nil {
		c.recoveryFailure(f, err)
		return
	}
	if !c.recoveryStage(f, "establish", 1) {
		return
	}
	if err := c.waitRecovery(f, voiceRecoveryTimeout, false); err != nil {
		c.recoveryFailure(f, err)
	}
}
