package discordbot

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

type voiceStageError struct {
	stage string
	err   error
}

func (e *voiceStageError) Error() string { return "voice " + e.stage + ": " + e.err.Error() }
func (e *voiceStageError) Unwrap() error { return e.err }

// A transport owns exactly one immutable set of credentials, one UDP socket,
// and one DAVE session. Closing it never sends a guild voice-state update.
type discordVoiceTransport struct {
	mu       sync.Mutex
	invalid  atomic.Bool
	conn     voice.Conn
	dave     godave.Session
	speaking bool
	closed   sync.Once
	cleanup  func()
	state    voice.State
}

func (t *discordVoiceTransport) Invalidate() { t.invalid.Store(true) }
func (t *discordVoiceTransport) Ready() bool {
	return !t.invalid.Load() && t.conn.Gateway().Status() == voice.StatusReady && t.dave.Ready()
}
func (t *discordVoiceTransport) Resume(ctx context.Context) error {
	if t.invalid.Load() {
		return context.Canceled
	}
	return t.conn.Gateway().Open(ctx, t.state)
}
func (t *discordVoiceTransport) Close() {
	t.Invalidate()
	t.closed.Do(func() { t.mu.Lock(); defer t.mu.Unlock(); t.conn.Close(context.Background()); t.cleanup() })
}
func (t *discordVoiceTransport) Write(ctx context.Context, p []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.invalid.Load() {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !t.dave.Ready() {
		return errDaveNotReady
	}
	if !t.speaking {
		if err := t.conn.SetSpeaking(ctx, voice.SpeakingFlagMicrophone); err != nil {
			return err
		}
		t.speaking = true
	}
	_ = t.conn.UDP().SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := t.conn.UDP().Write(p)
	return err
}
func (t *discordVoiceTransport) Finish(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.invalid.Load() || !t.speaking {
		return
	}
	_ = t.conn.UDP().SetWriteDeadline(time.Now().Add(time.Second))
	for range 5 {
		if _, err := t.conn.UDP().Write(voice.SilenceAudioFrame); err != nil {
			break
		}
	}
	_ = t.conn.SetSpeaking(ctx, voice.SpeakingFlagNone)
	t.speaking = false
}

func (a *App) openVoiceTransport(ctx context.Context, state voice.State, closed func(error, bool), opts []voice.ConnConfigOpt) (voiceTransport, error) {
	return a.openVoiceTransportWithDialer(ctx, state, closed, opts, websocket.DefaultDialer)
}

func (a *App) openVoiceTransportWithDialer(ctx context.Context, state voice.State, closed func(error, bool), opts []voice.ConnConfigOpt, dialer *websocket.Dialer) (voiceTransport, error) {
	t := &discordVoiceTransport{state: state}
	described := make(chan error, 1)
	options := append([]voice.ConnConfigOpt(nil), opts...)
	options = append(options, voice.WithConnGatewayCreateFunc(func(dave godave.Session, event voice.EventHandlerFunc, _ voice.CloseHandlerFunc, opts ...voice.GatewayConfigOpt) voice.Gateway {
		t.dave = dave
		dave.SetChannelID(godave.ChannelID(*state.ChannelID))
		return newVoiceSocket(dave, func(g voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.invalid.Load() {
				return
			}
			handleVoiceDaveEvent(dave, op, data)
			// We own readiness, so do not use Conn.Open's completion channel.
			if d, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
				err := t.conn.UDP().SetSecretKey(d.Mode, d.SecretKey)
				select {
				case described <- err:
				default:
				}
				if err != nil {
					go closed(err, false)
				}
			} else {
				event(g, op, seq, data)
			}
		}, func(_ voice.Gateway, err error, reconnect bool) {
			if !t.invalid.Load() {
				closed(err, reconnect)
			}
		}, dialer)
	}))
	// NewConn remains useful for UDP discovery and protocol selection. Its
	// membership side effects are intentionally disconnected from the manager.
	t.conn = voice.NewConn(state.GuildID, state.UserID, func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }, func() {}, options...)
	t.cleanup = func() { a.closeDave(t.conn) }
	if err := t.conn.Gateway().Open(ctx, state); err != nil {
		t.Close()
		return nil, &voiceStageError{"gateway", err}
	}
	select {
	case <-ctx.Done():
		t.Close()
		return nil, &voiceStageError{"transport_key", ctx.Err()}
	case err := <-described:
		if err != nil {
			t.Close()
			return nil, &voiceStageError{"transport_key", err}
		}
	}
	for !t.dave.Ready() {
		select {
		case <-ctx.Done():
			t.Close()
			return nil, &voiceStageError{"dave", ctx.Err()}
		case <-time.After(10 * time.Millisecond):
		}
	}
	return t, nil
}
