package discordbot

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
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
}

func resumeAllowed(err error) bool {
	var ws *websocket.CloseError
	if errors.As(err, &ws) {
		return ws.Code == 4015 || ws.Code == 1006 || ws.Code == 1001
	}
	return !errors.Is(err, context.Canceled)
}

// Only transport loss or an expired session may initiate a new membership
// request. Protocol errors, kicks and rate limits must not trigger re-entry.
func reconnectAllowed(err error) bool {
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

func (c *voiceConnection) recoveryValidLocked(f *voiceRecoveryFlow) bool {
	return c.recovery == f && f.ctx.Err() == nil && !c.removed && c.phase != voiceClosed && c.phase != voiceFailed
}

// One supervisor owns the recovery budget. Transport generations may change
// within it, but never grant additional time or reset the rejoin limit.
func (c *voiceConnection) startRecoveryLocked(parent context.Context, resume, sendJoin, explicit bool) {
	c.cancelRecoveryLocked()
	if c.deadline.IsZero() {
		c.recoveryStart = c.now()
		c.deadline = c.now().Add(voiceRecoveryTimeout)
		c.rejoins = 0
	}
	c.recoverySerial++
	ctx, cancel := context.WithDeadline(parent, c.deadline)
	f := &voiceRecoveryFlow{id: c.recoverySerial, ctx: ctx, cancel: cancel, explicit: explicit}
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
	if errors.Is(err, errRejoinDenied) {
		c.failureReason = "permission_unconfirmed"
	}
	if errors.Is(err, errRejoinBudget) {
		c.failureReason = "retry_limit"
	}
	if errors.Is(err, context.DeadlineExceeded) && (f.stage == "voice_notifications" || f.stage == "rejoin_notifications") {
		if c.state == nil {
			c.failureStage = "membership_notification"
		} else if c.server == nil {
			c.failureStage = "server_notification"
		}
	}
	c.discardLocked()
	c.signalLocked(voiceFailed)
	c.cancelRecoveryLocked()
}

func (c *voiceConnection) waitRecovery(f *voiceRecoveryFlow, duration time.Duration, leaving bool) error {
	ctx, cancel := context.WithTimeout(f.ctx, duration)
	defer cancel()
	for {
		c.mu.Lock()
		if c.phase == voiceReady && !leaving {
			c.mu.Unlock()
			return nil
		}
		if !c.recoveryValidLocked(f) {
			c.mu.Unlock()
			return context.Canceled
		}
		if leaving && f.left {
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
func (c *voiceConnection) recoveryUpdate(f *voiceRecoveryFlow, leave bool) error {
	ctx, cancel := context.WithTimeout(f.ctx, voiceStageTimeout)
	defer cancel()
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
	if resume {
		for attempt := 1; attempt <= voiceResumeAttempts; attempt++ {
			if !c.recoveryStage(f, "resume", attempt) {
				return
			}
			c.mu.Lock()
			t := c.active
			c.resuming = true
			c.mu.Unlock()
			resumable, ok := t.(interface{ Resume(context.Context) error })
			if !ok {
				break
			}
			ctx, cancel := context.WithTimeout(f.ctx, voiceStageTimeout)
			err := resumable.Resume(ctx)
			cancel()
			c.mu.Lock()
			if !c.recoveryValidLocked(f) {
				c.mu.Unlock()
				return
			}
			if err == nil && t.Ready() {
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
		c.state = nil
		c.server = nil
		c.lastToken = ""
		c.statePending = true
		f.attemptErr = nil
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
	if err := c.waitRecovery(f, voiceStageTimeout, false); err == nil {
		return
	}
	c.mu.Lock()
	if !c.recoveryValidLocked(f) {
		c.mu.Unlock()
		return
	}
	// Once both notifications arrive, allow the handshake the remaining budget.
	connecting := c.phase == voiceConnecting
	c.mu.Unlock()
	if connecting {
		if !c.recoveryStage(f, "establish", 1) {
			return
		}
		if err := c.waitRecovery(f, voiceRecoveryTimeout, false); err == nil {
			return
		}
	}
	c.rejoinRecovery(f)
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
	if check == nil || !check(f.ctx, channel) {
		c.recoveryFailure(f, errRejoinDenied)
		return
	}
	c.mu.Lock()
	if !c.recoveryValidLocked(f) || c.channel != channel {
		c.mu.Unlock()
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
	f.leaving = true
	f.left = false
	f.attemptErr = nil
	f.stage = "leave_request"
	c.state = nil
	c.server = nil
	c.statePending = false
	c.signalLocked(voiceRecovering)
	c.mu.Unlock()
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
	c.mu.Lock()
	if !c.recoveryValidLocked(f) {
		c.mu.Unlock()
		return
	}
	f.leaving = false
	f.stage = "rejoin_request"
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
	if err := c.waitRecovery(f, voiceRecoveryTimeout, false); err != nil {
		c.recoveryFailure(f, err)
	}
}
