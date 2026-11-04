package discordbot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
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
	mu            sync.Mutex
	guild, user   snowflake.ID
	channel       snowflake.ID
	phase         voicePhase
	generation    uint64
	deadline      time.Time
	recoveryStart time.Time
	now           func() time.Time
	changed       chan struct{}
	cancel        context.CancelFunc
	timer         *time.Timer
	sendMu        sync.Mutex
	active        voiceTransport
	state         *gateway.EventVoiceStateUpdate
	server        *gateway.EventVoiceServerUpdate
	lastToken     string
	retiredTokens []string
	statePending  bool
	removed       bool
	resuming      bool
	update        voice.StateUpdateFunc
	remove        func()
	factory       func(context.Context, voice.State, func(error, bool)) (voiceTransport, error)
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
		c.update, c.remove = update, remove
		c.factory = func(ctx context.Context, state voice.State, closed func(error, bool)) (voiceTransport, error) {
			return a.openVoiceTransport(ctx, state, closed, opts)
		}
		return c
	}))
}

func (c *voiceConnection) signalLocked(phase voicePhase) {
	if c.phase != phase {
		slog.Info("voice state", "guild", c.guild, "channel", c.channel, "generation", c.generation, "from", c.phase, "to", phase)
	}
	c.phase = phase
	if phase == voiceReady && !c.recoveryStart.IsZero() {
		slog.Info("voice recovery complete", "guild", c.guild, "channel", c.channel, "generation", c.generation, "duration", c.now().Sub(c.recoveryStart))
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
	c.timer = time.AfterFunc(max(0, c.deadline.Sub(c.now())), func() { c.mu.Lock(); defer c.mu.Unlock(); c.expireLocked() })
}

// expireLocked is also called by tests with a deterministic clock.
func (c *voiceConnection) expireLocked() {
	if c.phase != voiceReady && c.phase != voiceClosed && c.phase != voiceFailed && !c.deadline.IsZero() && !c.now().Before(c.deadline) {
		c.discardLocked()
		c.signalLocked(voiceFailed)
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
	c.retiredTokens = append(c.retiredTokens, state.Token)
	if len(c.retiredTokens) > 128 {
		c.retiredTokens = c.retiredTokens[len(c.retiredTokens)-128:]
	}
	c.server = nil
	c.statePending = false
	c.discardLocked()
	generation := c.generation
	ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
	c.cancel = cancel
	c.signalLocked(voiceConnecting)
	go func() {
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
			logOperationFailure("voice."+stage, err)
			c.discardLocked()
			c.signalLocked(voiceFailed)
			c.mu.Unlock()
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
		c.mu.Unlock()
	}()
}
func (c *voiceConnection) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	if e.GuildID != c.guild || e.UserID != c.user {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase == voiceClosed {
		return
	}
	if e.ChannelID == nil {
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
	changed := moved || c.state == nil || c.state.SessionID != e.SessionID
	slog.Info("voice membership received", "guild", c.guild, "channel", id, "generation", c.generation, "phase", c.phase, "moved", moved, "session_changed", c.state == nil || c.state.SessionID != e.SessionID, "server_pending", c.server != nil)
	c.channel = id
	c.state = &e
	if c.removed || c.phase == voiceFailed {
		return
	} // only explicit Open can restart
	if changed {
		c.statePending = true
		c.beginLocked(moved, voiceWaiting)
	}
	c.launchLocked()
}
func (c *voiceConnection) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	if e.GuildID != c.guild {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Only comparisons are logged; credentials and endpoint values stay private.
	disposition := "accepted"
	defer func() {
		slog.Info("voice server update received", "guild", c.guild, "channel", c.channel, "generation", c.generation, "phase", c.phase, "disposition", disposition, "endpoint_present", e.Endpoint != nil, "membership_pending", c.statePending)
	}()
	if c.phase == voiceClosed || c.phase == voiceFailed {
		disposition = "terminal_state"
		return
	}
	if e.Endpoint == nil {
		disposition = "endpoint_unavailable"
		c.server = nil
		c.beginLocked(false, voiceRecovering)
		return
	}
	if e.Token == c.lastToken || (c.server != nil && c.server.Token == e.Token) {
		disposition = "duplicate_token"
		return
	}
	for _, token := range c.retiredTokens {
		if token == e.Token {
			disposition = "retired_token"
			return
		}
	}
	endpoint := *e.Endpoint
	e.Endpoint = &endpoint
	c.server = &e
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
	slog.Info("voice transport closed", "guild", c.guild, "generation", generation, "code", code)
	if code == 4015 && c.active != nil {
		if resumable, ok := c.active.(interface{ Resume(context.Context) error }); ok {
			if c.resuming {
				return
			}
			c.resuming = true
			if c.deadline.IsZero() {
				c.deadline = c.now().Add(voiceRecoveryTimeout)
			}
			ctx, cancel := context.WithDeadline(context.Background(), c.deadline)
			c.cancel = cancel
			c.signalLocked(voiceRecovering)
			c.armLocked()
			go func() {
				err := resumable.Resume(ctx)
				c.mu.Lock()
				defer c.mu.Unlock()
				c.expireLocked()
				if generation != c.generation || c.phase != voiceRecovering {
					return
				}
				if err != nil {
					c.discardLocked()
					c.signalLocked(voiceFailed)
				} else {
					c.resuming = false
					c.deadline = time.Time{}
					c.timer.Stop()
					c.signalLocked(voiceReady)
				}
			}()
			return
		}
	}
	// Terminal transport codes may be followed by membership/server updates.
	// Never emit a leave or rejoin request here.
	c.beginLocked(false, voiceRecovering)
	if code == 4014 || code == 4022 {
		return
	}
	// Authentication, protocol and rate-limit failures require user intervention.
	if !reconnect {
		c.signalLocked(voiceFailed)
		return
	}
	// Obtain fresh credentials instead of resuming a discarded DAVE session.
	c.statePending = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.sendMu.Lock()
		defer c.sendMu.Unlock()
		c.mu.Lock()
		valid := c.phase == voiceRecovering
		channel := c.channel
		c.mu.Unlock()
		if valid && channel != 0 {
			_ = c.update(ctx, c.guild, &channel, false, true)
		}
	}()
}

func (c *voiceConnection) Open(ctx context.Context, channel snowflake.ID, mute, deaf bool) error {
	c.mu.Lock()
	if c.phase == voiceClosed {
		c.mu.Unlock()
		return context.Canceled
	}
	c.removed = false
	c.statePending = true
	c.state = nil // explicit join must pair with its new membership notification
	c.server = nil
	c.lastToken = ""
	c.beginLocked(true, voiceWaiting)
	c.mu.Unlock()
	c.sendMu.Lock()
	c.mu.Lock()
	valid := c.phase != voiceClosed
	c.mu.Unlock()
	var err error
	if valid {
		err = c.update(ctx, c.guild, &channel, mute, deaf)
	} else {
		err = context.Canceled
	}
	c.sendMu.Unlock()
	if err != nil {
		return err
	}
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
	c.mu.Lock()
	if c.phase == voiceClosed {
		c.mu.Unlock()
		return
	}
	removed := c.removed
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
		_ = c.update(ctx, c.guild, nil, false, false)
	}
	c.remove()
}
