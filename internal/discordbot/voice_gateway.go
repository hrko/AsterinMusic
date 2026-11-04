package discordbot

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

// disgo 0.19.6 closes the entire Conn on a terminal voice gateway close, even
// when Discord is moving it. It also rejects a new server update while the old
// gateway is connected. Keep the Conn/UDP sender, but replace gateway generations.
func (a *App) voiceMoveOptions() bot.ConfigOpt {
	return bot.WithVoiceManagerConfigOpts(voice.WithConnCreateFunc(func(guild, user snowflake.ID, update voice.StateUpdateFunc, remove func(), opts ...voice.ConnConfigOpt) voice.Conn {
		var conn voice.Conn
		opts = append(opts, voice.WithConnGatewayCreateFunc(func(dave godave.Session, event voice.EventHandlerFunc, closeHandler voice.CloseHandlerFunc, options ...voice.GatewayConfigOpt) voice.Gateway {
			return &movingGateway{factory: func(e voice.EventHandlerFunc, c voice.CloseHandlerFunc) voice.Gateway {
				return voice.NewGateway(dave, e, c, options...)
			}, event: event, onClose: closeHandler,
				currentChannel: func() snowflake.ID {
					state, ok := a.client.Caches.VoiceState(guild, user)
					if !ok || state.ChannelID == nil {
						return 0
					}
					return *state.ChannelID
				},
				rekey: func(d voice.GatewayMessageDataSessionDescription) error {
					return conn.UDP().SetSecretKey(d.Mode, d.SecretKey)
				},
				closeUDP: func() { _ = conn.UDP().Close() },
			}
		}))
		conn = voice.NewConn(guild, user, update, remove, opts...)
		return conn
	}))
}

type movingGateway struct {
	openMu         sync.Mutex
	mu             sync.Mutex
	events         sync.Mutex
	base           voice.Gateway
	generation     uint64
	state          voice.State
	closed         bool
	described      bool
	ready          bool
	factory        func(voice.EventHandlerFunc, voice.CloseHandlerFunc) voice.Gateway
	event          voice.EventHandlerFunc
	onClose        voice.CloseHandlerFunc
	currentChannel func() snowflake.ID
	rekey          func(voice.GatewayMessageDataSessionDescription) error
	closeUDP       func()
}

func (g *movingGateway) Open(ctx context.Context, state voice.State) error {
	g.openMu.Lock()
	defer g.openMu.Unlock()
	g.events.Lock()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		g.events.Unlock()
		return voice.ErrGatewayNotConnected
	}
	// Copy the pointed-to channel ID; the connection's state is mutable.
	if state.ChannelID != nil {
		id := *state.ChannelID
		state.ChannelID = &id
	}
	if g.base != nil && g.ready && sameVoiceState(g.state, state) {
		g.mu.Unlock()
		g.events.Unlock()
		return nil
	}
	old := g.base
	g.generation++
	generation := g.generation
	g.state = state
	g.ready = false
	base := g.factory(func(v voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
		g.handleEvent(generation, v, op, seq, data)
	}, func(v voice.Gateway, err error, reconnect bool) { g.handleClose(generation, v, err, reconnect) })
	g.base = base
	g.mu.Unlock()
	g.events.Unlock()
	if old != nil {
		old.Close()
		if g.closeUDP != nil {
			g.closeUDP()
		}
	}
	err := base.Open(ctx, state)
	g.mu.Lock()
	stale := g.closed || generation != g.generation
	g.mu.Unlock()
	if stale {
		base.Close()
		return context.Canceled
	}
	return err
}

// Serialize audio with UDP reopening/key installation. The upstream UDP
// implementation does not protect all encryption/header fields across Open.
func withReadyVoiceGateway(gateway voice.Gateway, send func() error) error {
	if g, ok := gateway.(*movingGateway); ok {
		g.events.Lock()
		defer g.events.Unlock()
	}
	if gateway.Status() != voice.StatusReady {
		return errDaveNotReady
	}
	return send()
}

func sameVoiceState(a, b voice.State) bool {
	return a.ChannelID != nil && b.ChannelID != nil && *a.ChannelID == *b.ChannelID && a.SessionID == b.SessionID && a.Token == b.Token && a.Endpoint == b.Endpoint
}

func (g *movingGateway) handleEvent(generation uint64, v voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
	g.events.Lock()
	defer g.events.Unlock()
	g.mu.Lock()
	if g.closed || generation != g.generation {
		g.mu.Unlock()
		return
	}
	already := g.described
	g.mu.Unlock()
	if d, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
		// Only the initial Conn.Open consumes openedChan. Repeated descriptions
		// must install the key without filling that channel and blocking the reader.
		if already {
			if err := g.rekey(d); err != nil {
				logOperationFailure("voice.move_rekey", err)
				return
			}
		} else {
			g.event(v, op, seq, data)
		}
		g.mu.Lock()
		g.described = true
		g.ready = true
		g.mu.Unlock()
		return
	}
	g.event(v, op, seq, data)
}

func (g *movingGateway) handleClose(generation uint64, v voice.Gateway, err error, reconnect bool) {
	var closed *websocket.CloseError
	if errors.As(err, &closed) && closed.Code == 4014 {
		// Gateway and voice-state events travel independently. Briefly allow the
		// cache to identify a move, but never request rejoin after a real removal.
		deadline := time.Now().Add(time.Second)
		for {
			g.mu.Lock()
			if g.closed || generation != g.generation {
				g.mu.Unlock()
				return
			}
			channel := g.state.ChannelID
			g.ready = false
			g.mu.Unlock()
			current := g.currentChannel()
			if current != 0 && channel != nil && current != *channel {
				return
			}
			if current == 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	g.events.Lock()
	defer g.events.Unlock()
	g.mu.Lock()
	valid := !g.closed && generation == g.generation
	if valid {
		g.ready = false
	}
	g.mu.Unlock()
	if valid {
		g.onClose(v, err, reconnect)
	}
}

func (g *movingGateway) snapshot() voice.Gateway { g.mu.Lock(); defer g.mu.Unlock(); return g.base }
func (g *movingGateway) Close() {
	g.mu.Lock()
	g.closed = true
	g.ready = false
	g.generation++
	base := g.base
	g.mu.Unlock()
	if base != nil {
		base.Close()
	}
}
func (g *movingGateway) CloseWithCode(code int, message string) {
	g.mu.Lock()
	g.closed = true
	g.ready = false
	g.generation++
	base := g.base
	g.mu.Unlock()
	if base != nil {
		base.CloseWithCode(code, message)
	}
}
func (g *movingGateway) SSRC() uint32 {
	if base := g.snapshot(); base != nil {
		return base.SSRC()
	}
	return 0
}
func (g *movingGateway) Latency() time.Duration {
	if base := g.snapshot(); base != nil {
		return base.Latency()
	}
	return 0
}
func (g *movingGateway) Status() voice.Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return voice.StatusDisconnected
	}
	if !g.ready {
		return voice.StatusConnecting
	}
	return g.base.Status()
}
func (g *movingGateway) Send(ctx context.Context, op voice.Opcode, data voice.GatewayMessageData) error {
	if base := g.snapshot(); base != nil {
		return base.Send(ctx, op, data)
	}
	return voice.ErrGatewayNotConnected
}
