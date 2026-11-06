package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/trace"
)

type voiceStageError struct {
	stage string
	err   error
}

// Bound queued protocol work independently of reader/heartbeat progress.
const voiceEventCapacity = 64

func (e *voiceStageError) Error() string { return "voice " + e.stage + ": " + e.err.Error() }
func (e *voiceStageError) Unwrap() error { return e.err }

// A transport owns exactly one immutable set of credentials, one UDP socket,
// and one DAVE session. Closing it never sends a guild voice-state update.
type discordVoiceTransport struct {
	tracer       trace.Tracer
	traceCause   trace.SpanContext
	mu           sync.Mutex
	invalid      atomic.Bool
	conn         voice.Conn
	dave         godave.Session
	speaking     bool
	closed       sync.Once
	cleanup      func()
	state        voice.State
	cancelEvents context.CancelFunc
	eventsDone   chan struct{}
}

func (t *discordVoiceTransport) Invalidate() {
	t.invalid.Store(true)
	if t.cancelEvents != nil {
		t.cancelEvents()
	}
}
func (t *discordVoiceTransport) Ready() bool {
	return !t.invalid.Load() && t.conn.Gateway().Status() == voice.StatusReady && t.dave.Ready()
}
func (t *discordVoiceTransport) Resume(ctx context.Context) error {
	if t.invalid.Load() {
		return context.Canceled
	}
	if err := t.conn.Gateway().(*voiceSocket).Resume(ctx, t.state); err != nil {
		return err
	}
	t.mu.Lock()
	t.speaking = false // Re-announce speaking before sending on the resumed socket.
	t.traceCause = trace.SpanContextFromContext(ctx)
	t.mu.Unlock()
	return nil
}
func (t *discordVoiceTransport) Close() {
	t.Invalidate()
	t.closed.Do(func() {
		// Interrupt network I/O before waiting for the sender/event worker.
		t.conn.Close(context.Background())
		<-t.eventsDone
		t.mu.Lock()
		defer t.mu.Unlock()
		t.cleanup()
	})
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
		opts := []trace.SpanStartOption{}
		if t.traceCause.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: t.traceCause}))
		}
		speakCtx, span := tracerOrNoop(t.tracer).Start(ctx, "voice.speaking", opts...)
		err := t.conn.SetSpeaking(speakCtx, voice.SpeakingFlagMicrophone)
		finishTrace(span, err)
		if err != nil {
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
	logger := logging.Component(a.logger, "voice").With("guild_id", state.GuildID.String())
	t := &discordVoiceTransport{state: state, tracer: a.tracer, traceCause: trace.SpanContextFromContext(ctx)}
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	t.cancelEvents, t.eventsDone = cancelEvents, make(chan struct{})
	// This worker survives successful establishment and Resume. In particular,
	// SessionDescription and subsequent DAVE events share one ordered queue.
	type transportEvent struct {
		gateway voice.Gateway
		op      voice.Opcode
		seq     int
		data    voice.GatewayMessageData
	}
	events := make(chan transportEvent, voiceEventCapacity)
	var handle voice.EventHandlerFunc
	described := make(chan error, 1)
	failed := make(chan error, 1)
	var established atomic.Bool
	var failOnce sync.Once
	var stage atomic.Value
	var daveDiagnostic atomic.Value
	stage.Store("gateway")
	progress := func(observation context.Context, name string) { stage.Store(name); traceVoiceStage(observation, name) }
	failHandshake := func(stage string, err error) {
		failOnce.Do(func() { failed <- &voiceStageError{stage, err} })
	}
	failure := func(err error) error {
		select {
		case original := <-failed:
			err = original
		default:
		}
		var detail *voiceStageError
		if !errors.As(err, &detail) {
			err = &voiceStageError{stage.Load().(string), err}
		}
		attrs := []any{"event", "voice.handshake.failed", "stage", stage.Load().(string), "reason", failureClass(err)}
		if snapshot := daveDiagnostic.Load(); snapshot != nil {
			attrs = append(attrs, snapshot.([]any)...)
		}
		recordVoiceDiagnostic(ctx, logger, slog.LevelDebug, "voice handshake failed", attrs...)
		t.Close()
		return err
	}
	options := []voice.ConnConfigOpt{voice.WithUDPConnCreateFunc(func(dave godave.Session, _ voice.SsrcLookupFunc, _ ...voice.UDPConnConfigOpt) voice.UDPConn {
		return &voiceUDP{dave: dave}
	})}
	options = append(options, opts...)
	options = append(options, voice.WithConnGatewayCreateFunc(func(dave godave.Session, event voice.EventHandlerFunc, _ voice.CloseHandlerFunc, opts ...voice.GatewayConfigOpt) voice.Gateway {
		t.dave = dave
		dave.SetChannelID(godave.ChannelID(*state.ChannelID))
		handle = func(g voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
			if t.invalid.Load() {
				return
			}
			if op == voice.OpcodeSessionDescription || int(op) >= 21 {
				defer func() {
					attrs := []any{"event", "voice.dave.event", "opcode", int(op), "sequence", seq}
					if diagnostic, ok := dave.(interface{ VoiceDiagnostic() []any }); ok {
						snapshot := diagnostic.VoiceDiagnostic()
						daveDiagnostic.Store(snapshot)
						attrs = append(attrs, snapshot...)
					}
					recordVoiceDiagnostic(ctx, logger, slog.LevelDebug, "voice DAVE event processed", attrs...)
				}()
			}
			if d, ok := data.(voice.GatewayMessageDataReady); ok {
				progress(ctx, "udp_discovery")
				discoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				stop := context.AfterFunc(eventCtx, cancel)
				address, port, err := t.conn.UDP().Open(discoveryCtx, d.IP, d.Port, d.SSRC)
				stop()
				cancel()
				if err != nil {
					failHandshake("udp_discovery", err)
					return
				}
				progress(ctx, "select_protocol")
				mode, err := voice.ChooseEncryptionMode(d.Modes)
				if err == nil {
					err = g.Send(ctx, voice.OpcodeSelectProtocol, voice.GatewayMessageDataSelectProtocol{
						Protocol: voice.ProtocolUDP,
						Data:     voice.GatewayMessageDataSelectProtocolData{Address: address, Port: port, Mode: mode},
					})
				}
				if err != nil {
					failHandshake("select_protocol", err)
					return
				}
				progress(ctx, "session_description")
				return
			}
			// We own readiness, so do not use Conn.Open's completion channel.
			if d, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
				progress(ctx, "transport_key")
				err := t.conn.UDP().SetSecretKey(d.Mode, d.SecretKey)
				if err != nil {
					failHandshake("transport_key", err)
					if established.Load() {
						closed(err, false)
					}
					return
				}
				progress(ctx, "dave")
				handleVoiceDaveEvent(dave, op, data)
				select {
				case described <- nil:
				default:
				}
			} else {
				handleVoiceDaveEvent(dave, op, data)
				event(g, op, seq, data)
			}
		}
		socket := newVoiceSocket(dave, func(g voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
			if t.invalid.Load() {
				return
			}
			select {
			case events <- transportEvent{g, op, seq, data}:
			case <-eventCtx.Done():
			default:
				err := errors.New("voice event queue overflow")
				t.Invalidate()
				failHandshake("event_queue", err)
				if established.Load() {
					closed(err, false)
				}
			}
		}, func(_ voice.Gateway, err error, reconnect bool) {
			if !t.invalid.Load() {
				failHandshake(stage.Load().(string), err)
				if established.Load() {
					closed(err, reconnect)
				}
			}
		}, dialer)
		socket.progress = progress
		return socket
	}))
	// Reuse disgo's UDP and event types, but own discovery/protocol errors.
	t.conn = voice.NewConn(state.GuildID, state.UserID, func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }, func() {}, options...)
	t.cleanup = func() { a.closeDave(t.conn) }
	go func() {
		defer close(t.eventsDone)
		for {
			select {
			case <-eventCtx.Done():
				return
			case e := <-events:
				if eventCtx.Err() != nil {
					return
				}
				handle(e.gateway, e.op, e.seq, e.data)
			}
		}
	}()
	progress(ctx, "gateway")
	if err := t.conn.Gateway().Open(ctx, state); err != nil {
		return nil, failure(err)
	}
	select {
	case <-ctx.Done():
		return nil, failure(ctx.Err())
	case err := <-failed:
		return nil, failure(err)
	case err := <-described:
		if err != nil {
			return nil, failure(err)
		}
	}
	for !t.dave.Ready() {
		select {
		case <-ctx.Done():
			return nil, failure(ctx.Err())
		case err := <-failed:
			return nil, failure(err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	established.Store(true)
	select {
	case err := <-failed:
		return nil, failure(err)
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, failure(err)
	}
	return t, nil
}
