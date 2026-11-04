package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

var errVoiceRecovery = errors.New("voice connection recovery failed")
var errVoiceRemoved = errors.New("voice membership removed")

const voiceRecoveryTimeout = 30 * time.Second

type voicePhase string

const (
	voiceIdle       voicePhase = "idle"
	voiceWaiting    voicePhase = "waiting"
	voiceConnecting voicePhase = "connecting"
	voiceReady      voicePhase = "ready"
	voiceRecovering voicePhase = "recovering"
	voiceFailed     voicePhase = "failed"
	voiceClosed     voicePhase = "closed"
)

// All decisions are serialized by mu; network work runs outside it. A lease is
// invalidated before its asynchronous teardown, so late callbacks cannot revive it.
type voiceConnection struct {
	recovery           *voiceRecoveryFlow
	recoverySerial     uint64
	rejoins            int
	rejoinTimes        []time.Time
	canRejoin          func(context.Context, snowflake.ID) bool
	mute, deaf         bool
	failureStage       string
	failureReason      string
	failureCode        int
	diagnosticMu       sync.Mutex
	diagnostics        []voiceDiagnostic
	diagnosticOverflow map[string]voiceDiagnostic
	logger             *slog.Logger
	mu                 sync.Mutex
	guild, user        snowflake.ID
	channel            snowflake.ID
	phase              voicePhase
	generation         uint64
	deadline           time.Time
	recoveryStart      time.Time
	now                func() time.Time
	changed            chan struct{}
	cancel             context.CancelFunc
	timer              *time.Timer
	sendMu             sync.Mutex
	active             voiceTransport
	state              *gateway.EventVoiceStateUpdate
	server             *gateway.EventVoiceServerUpdate
	lastToken          string
	statePending       bool
	removed            bool
	resuming           bool
	update             voice.StateUpdateFunc
	remove             func()
	factory            func(context.Context, voice.State, func(error, bool)) (voiceTransport, error)
}

type voiceTransport interface {
	Write(context.Context, []byte) error
	Finish(context.Context)
	Close()
	Invalidate()
	Ready() bool
}

func newVoiceConnection(guild, user snowflake.ID) *voiceConnection {
	return &voiceConnection{guild: guild, user: user, phase: voiceIdle, now: time.Now, changed: make(chan struct{})}
}

func (a *App) voiceConnectionOptions() bot.ConfigOpt {
	return bot.WithVoiceManagerConfigOpts(voice.WithConnCreateFunc(func(guild, user snowflake.ID, update voice.StateUpdateFunc, remove func(), opts ...voice.ConnConfigOpt) voice.Conn {
		c := newVoiceConnection(guild, user)
		c.logger = logging.Component(a.logger, "voice").With("guild_id", guild.String())
		c.update, c.remove = update, remove
		c.canRejoin = func(ctx context.Context, channel snowflake.ID) bool {
			if ctx.Err() != nil {
				return false
			}
			vc, ok := a.client.Caches.GuildAudioChannel(channel)
			if !ok || vc.GuildID() != guild {
				return false
			}
			member, ok := a.client.Caches.Member(guild, user)
			if !ok {
				return false
			}
			return a.client.Caches.MemberPermissionsInChannel(vc, member).Has(discord.PermissionViewChannel, discord.PermissionConnect)
		}
		c.factory = func(ctx context.Context, state voice.State, closed func(error, bool)) (voiceTransport, error) {
			return a.openVoiceTransport(ctx, state, closed, opts)
		}
		return c
	}))
}

func (c *voiceConnection) signalLocked(phase voicePhase) {
	if c.phase != phase {
		c.recordDiagnosticLocked(slog.LevelDebug, "voice state", "event", "voice.state.changed", "channel_id", c.channel.String(), "generation", c.generation, "from", string(c.phase), "to", string(phase))
		if phase == voiceReady {
			c.recordDiagnosticLocked(slog.LevelInfo, "voice connection ready", "event", "voice.connection.ready", "channel_id", c.channel.String(), "generation", c.generation)
		}
		if phase == voiceFailed {
			if c.removed {
				c.recordDiagnosticLocked(slog.LevelInfo, "voice membership removed", "event", "voice.membership.removed", "generation", c.generation, "action", "hold_playback", "queue_retained", true)
			} else {
				attrs := []any{"event", "voice.recovery.failed", "channel_id", c.channel.String(), "generation", c.generation, "action", "hold_playback", "queue_retained", true}
				if c.failureStage != "" {
					attrs = append(attrs, "stage", c.failureStage)
				}
				if c.failureReason != "" {
					attrs = append(attrs, "reason", c.failureReason)
				}
				if c.failureCode != 0 {
					attrs = append(attrs, "close_code", c.failureCode)
				}
				c.recordDiagnosticLocked(slog.LevelError, "voice connection failed", attrs...)
			}
		}
	}
	c.phase = phase
	if phase == voiceReady {
		c.failureStage = ""
		c.failureReason = ""
		c.failureCode = 0
	}
	if phase == voiceReady && !c.recoveryStart.IsZero() {
		c.recordDiagnosticLocked(slog.LevelInfo, "voice recovery complete", "event", "voice.recovery.completed", "channel_id", c.channel.String(), "generation", c.generation, "duration_ms", c.now().Sub(c.recoveryStart).Milliseconds())
		c.recoveryStart = time.Time{}
	}
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *voiceConnection) discardLocked() {
	c.resuming = false
	c.generation++
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if t := c.active; t != nil {
		t.Invalidate()
		c.active = nil
		go t.Close()
	}
}
func (c *voiceConnection) beginLocked(reset bool, phase voicePhase) {
	c.discardLocked()
	if reset || c.deadline.IsZero() {
		c.rejoins = 0
		c.recoveryStart = c.now()
		c.deadline = c.now().Add(voiceRecoveryTimeout)
	}
	c.signalLocked(phase)
	c.armLocked()
}
func (c *voiceConnection) armLocked() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(max(0, c.deadline.Sub(c.now())), func() { c.mu.Lock(); c.expireLocked(); c.mu.Unlock(); c.flushDiagnostics() })
}

// expireLocked is also called by tests with a deterministic clock.
func (c *voiceConnection) expireLocked() {
	if c.phase != voiceReady && c.phase != voiceClosed && c.phase != voiceFailed && !c.deadline.IsZero() && !c.now().Before(c.deadline) {
		c.failureStage = "recovery"
		if c.recovery != nil {
			c.failureStage = c.recovery.stage
		}
		c.failureReason = "timeout"
		c.discardLocked()
		c.signalLocked(voiceFailed)
		c.cancelRecoveryLocked()
	}
}
func (c *voiceConnection) launchLocked() {
	if c.phase == voiceClosed || c.phase == voiceFailed || c.removed || !c.statePending || c.state == nil || c.server == nil || c.server.Endpoint == nil {
		return
	}
	state := voice.State{GuildID: c.guild, UserID: c.user, ChannelID: c.state.ChannelID, SessionID: c.state.SessionID, Token: c.server.Token, Endpoint: *c.server.Endpoint}
	if state.ChannelID == nil || state.SessionID == "" || state.Token == "" {
		return
	}
	c.lastToken = state.Token
	c.server = nil
	c.statePending = false
	c.discardLocked()
	generation := c.generation
	ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
	c.cancel = cancel
	c.signalLocked(voiceConnecting)
	go func() {
		defer c.flushDiagnostics()
		t, err := c.factory(ctx, state, func(err error, reconnect bool) { c.transportClosed(generation, err, reconnect) })
		c.mu.Lock()
		c.expireLocked()
		if generation != c.generation || c.phase != voiceConnecting {
			c.mu.Unlock()
			if t != nil {
				t.Close()
			}
			return
		}
		if err != nil {
			stage := "establish"
			var detail *voiceStageError
			if errors.As(err, &detail) {
				stage = detail.stage
			}
			c.failureStage = stage
			c.failureReason = failureClass(err)
			if c.recovery != nil {
				c.recovery.attemptErr = err
				c.discardLocked()
				c.signalLocked(voiceWaiting)
				c.mu.Unlock()
				if t != nil {
					t.Close()
				}
				return
			}
			c.discardLocked()
			c.signalLocked(voiceFailed)
			c.mu.Unlock()
			logging.OrDiscard(c.logger).DebugContext(ctx, "voice establishment attempt failed", "event", "voice.establish.failed", "stage", stage, "reason", failureClass(err), "generation", generation)
			if t != nil {
				t.Close()
			}
			return
		}
		c.active = t
		c.deadline = time.Time{}
		if c.timer != nil {
			c.timer.Stop()
		}
		c.signalLocked(voiceReady)
		c.cancelRecoveryLocked()
		c.mu.Unlock()
	}()
}
func (c *voiceConnection) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	defer c.flushDiagnostics()
	if e.GuildID != c.guild || e.UserID != c.user {
		return
	}
	c.mu.Lock()
	var diagnostic func()
	defer func() {
		c.mu.Unlock()
		if diagnostic != nil {
			diagnostic()
		}
	}()
	if c.phase == voiceClosed {
		return
	}
	if e.ChannelID == nil {
		if f := c.recovery; f != nil && f.leaving {
			f.left = true
			c.state = nil
			c.server = nil
			c.signalLocked(voiceRecovering)
			return
		}
		c.cancelRecoveryLocked()
		c.channel = 0
		c.removed = true
		c.state = nil
		c.server = nil
		c.discardLocked()
		c.signalLocked(voiceFailed)
		return
	}
	id := *e.ChannelID
	e.ChannelID = &id
	moved := c.channel != id
	if f := c.recovery; f != nil && f.leaving && !moved {
		// An old membership echo cannot complete the new connection before
		// the outstanding leave has been acknowledged.
		return
	}
	changed := moved || c.state == nil || c.state.SessionID != e.SessionID
	attrs := []any{"event", "voice.membership.received", "channel_id", id.String(), "generation", c.generation, "phase", string(c.phase), "moved", moved, "session_changed", c.state == nil || c.state.SessionID != e.SessionID, "server_pending", c.server != nil}
	diagnostic = func() { logging.OrDiscard(c.logger).Debug("voice membership received", attrs...) }
	c.channel = id
	c.state = &e
	if c.removed || c.phase == voiceFailed {
		return
	} // only explicit Open can restart
	if changed {
		if moved {
			c.cancelRecoveryLocked()
		}
		c.statePending = true
		c.beginLocked(false, voiceWaiting)
	}
	c.launchLocked()
}
func (c *voiceConnection) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	defer c.flushDiagnostics()
	if e.GuildID != c.guild {
		return
	}
	c.mu.Lock()
	// Only comparisons are logged; credentials and endpoint values stay private.
	disposition := "accepted"
	defer func() {
		attrs := []any{"event", "voice.server.received", "channel_id", c.channel.String(), "generation", c.generation, "phase", string(c.phase), "disposition", disposition, "endpoint_present", e.Endpoint != nil, "membership_pending", c.statePending}
		c.mu.Unlock()
		logging.OrDiscard(c.logger).Debug("voice server update received", attrs...)
	}()
	if c.phase == voiceClosed || c.phase == voiceFailed {
		disposition = "terminal_state"
		return
	}
	if f := c.recovery; f != nil && f.leaving {
		disposition = "awaiting_leave_confirmation"
		return
	}
	if e.Endpoint == nil {
		c.cancelRecoveryLocked()
		disposition = "endpoint_unavailable"
		c.server = nil
		c.beginLocked(false, voiceRecovering)
		return
	}
	if c.server != nil && c.server.Token == e.Token && c.server.Endpoint != nil && *c.server.Endpoint == *e.Endpoint {
		disposition = "duplicate_pending_notification"
		return
	}
	if !c.statePending && c.phase == voiceConnecting && e.Token == c.lastToken {
		disposition = "duplicate_connecting_notification"
		return
	}
	endpoint := *e.Endpoint
	e.Endpoint = &endpoint
	c.server = &e
	// A fresh server notification may carry the same token across VC moves.
	// If it precedes membership, retain it without disturbing the ready socket.
	// Never reuse the consumed notification merely because membership changed.
	if !c.statePending && e.Token == c.lastToken {
		disposition = "awaiting_membership"
		return
	}
	if c.phase == voiceReady || c.phase == voiceConnecting {
		c.beginLocked(false, voiceWaiting)
	}
	c.launchLocked()
}
func (c *voiceConnection) transportClosed(generation uint64, err error, reconnect bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation || c.phase == voiceClosed || c.phase == voiceFailed {
		return
	}
	var ws *websocket.CloseError
	code := 0
	if errors.As(err, &ws) {
		code = ws.Code
	}
	c.failureStage = "transport"
	c.failureReason = failureClass(err)
	c.failureCode = code
	c.recordDiagnosticLocked(slog.LevelDebug, "voice transport closed", "event", "voice.transport.closed", "generation", generation, "code", code)
	if c.recovery != nil && c.resuming {
		// The Resume call receives its own socket error. Its supervisor owns
		// retries; a second callback must not start another recovery flow.
		return
	}
	if resumeAllowed(err) && c.active != nil {
		if _, ok := c.active.(interface{ Resume(context.Context) error }); ok {
			c.resuming = true
			c.signalLocked(voiceRecovering)
			c.startRecoveryLocked(context.Background(), true, false, false)
			return
		}
	}
	// Terminal transport codes may be followed by membership/server updates.
	// Never emit a leave or rejoin request here.
	c.beginLocked(false, voiceRecovering)
	if code == 4014 || code == 4022 {
		c.cancelRecoveryLocked()
		return
	}
	// Authentication, protocol and rate-limit failures require user intervention.
	if !reconnect || !reconnectAllowed(err) {
		c.cancelRecoveryLocked()
		c.signalLocked(voiceFailed)
		return
	}
	c.startRecoveryLocked(context.Background(), false, true, false)
}

func (c *voiceConnection) Open(ctx context.Context, channel snowflake.ID, mute, deaf bool) error {
	c.mu.Lock()
	if c.phase == voiceClosed {
		c.mu.Unlock()
		return context.Canceled
	}
	c.removed = false
	c.cancelRecoveryLocked()
	c.channel = channel
	c.mute, c.deaf = mute, deaf
	c.rejoins = 0
	c.failureStage = ""
	c.failureReason = ""
	c.failureCode = 0
	c.statePending = true
	c.state = nil // explicit join must pair with its new membership notification
	c.server = nil
	c.lastToken = ""
	c.beginLocked(true, voiceWaiting)
	c.startRecoveryLocked(ctx, false, true, true)
	c.mu.Unlock()
	return c.waitReady(ctx)
}
func (c *voiceConnection) waitReady(ctx context.Context) error {
	for {
		c.mu.Lock()
		c.expireLocked()
		phase, removed, ch := c.phase, c.removed, c.changed
		if phase == voiceRecovering && !c.resuming && c.active != nil && c.active.Ready() {
			phase = voiceReady
		}
		c.mu.Unlock()
		if removed {
			return errVoiceRemoved
		}
		if phase == voiceReady {
			return nil
		}
		if phase == voiceFailed {
			return errVoiceRecovery
		}
		if phase == voiceClosed {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (c *voiceConnection) WriteAudio(ctx context.Context, packet []byte) error {
	for {
		if err := c.waitReady(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		t, gen := c.active, c.generation
		c.mu.Unlock()
		if t == nil {
			continue
		}
		err := t.Write(ctx, packet)
		if err == nil {
			c.mu.Lock()
			if gen == c.generation && c.phase == voiceRecovering && !c.resuming {
				c.deadline = time.Time{}
				if c.timer != nil {
					c.timer.Stop()
				}
				c.signalLocked(voiceReady)
			}
			c.mu.Unlock()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errDaveNotReady) {
			c.mu.Lock()
			if gen == c.generation {
				if c.deadline.IsZero() {
					c.deadline = c.now().Add(voiceRecoveryTimeout)
					c.armLocked()
				}
				c.signalLocked(voiceRecovering)
			}
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		c.mu.Lock()
		current := gen == c.generation
		c.mu.Unlock()
		if current {
			c.transportClosed(gen, err, true)
		}
	}
}
func (c *voiceConnection) FinishAudio(ctx context.Context) {
	c.mu.Lock()
	t := c.active
	c.mu.Unlock()
	if t != nil {
		t.Finish(ctx)
	}
}
func (c *voiceConnection) ChannelID() *snowflake.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channel == 0 {
		return nil
	}
	id := c.channel
	return &id
}
func (c *voiceConnection) GuildID() snowflake.ID { return c.guild }

// The manager requires voice.Conn, but raw media APIs would bypass generation
// checks. Fail explicitly on programmer misuse rather than embedding a nil Conn.
func (c *voiceConnection) Gateway() voice.Gateway           { panic("use voiceConnection.WriteAudio") }
func (c *voiceConnection) UDP() voice.UDPConn               { panic("use voiceConnection.WriteAudio") }
func (c *voiceConnection) UserIDBySSRC(uint32) snowflake.ID { return 0 }
func (c *voiceConnection) SetSpeaking(context.Context, voice.SpeakingFlags) error {
	return errors.New("speaking is managed by voiceConnection.WriteAudio")
}
func (c *voiceConnection) SetOpusFrameProvider(voice.OpusFrameProvider) {
	panic("use voiceConnection.WriteAudio")
}
func (c *voiceConnection) SetOpusFrameReceiver(voice.OpusFrameReceiver) {
	panic("audio receiving is unsupported")
}
func (c *voiceConnection) SetEventHandlerFunc(voice.EventHandlerFunc) {
	panic("voice events are managed internally")
}
func (c *voiceConnection) Failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireLocked()
	return c.phase == voiceFailed
}
func (c *voiceConnection) Close(ctx context.Context) {
	defer c.flushDiagnostics()
	c.mu.Lock()
	if c.phase == voiceClosed {
		c.mu.Unlock()
		return
	}
	removed := c.removed
	c.cancelRecoveryLocked()
	c.discardLocked()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.channel = 0
	c.signalLocked(voiceClosed)
	c.mu.Unlock()
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if !removed {
		if err := c.update(ctx, c.guild, nil, false, false); err != nil {
			logging.OrDiscard(c.logger).WarnContext(ctx, "voice leave request failed", "event", "voice.leave.failed", "reason", failureClass(err), "action", "local_connection_closed")
		}
	}
	c.remove()
}
