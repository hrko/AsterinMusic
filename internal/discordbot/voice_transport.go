package discordbot

import (
	"asterinmusic/internal/logging"
	"bytes"
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
	sendGate     chan struct{}
	done         <-chan struct{}
	invalid      atomic.Bool
	daveReady    atomic.Bool
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
	if dave, ok := t.dave.(interface{ Invalidate() }); ok {
		dave.Invalidate()
	}
	if t.cancelEvents != nil {
		t.cancelEvents()
	}
}
func (t *discordVoiceTransport) Ready() bool {
	return !t.invalid.Load() && t.conn.Gateway().Status() == voice.StatusReady && t.daveReady.Load()
}

// Serialize wire operations without holding the transport's state mutex.
// A canceled waiter must not wait for the previous network operation to return.
func (t *discordVoiceTransport) acquireSender(ctx context.Context) error {
	select {
	case t.sendGate <- struct{}{}:
		if t.invalid.Load() || ctx.Err() != nil {
			<-t.sendGate
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return context.Canceled
	}
}

func (t *discordVoiceTransport) Resume(ctx context.Context) error {
	if err := t.acquireSender(ctx); err != nil {
		return err
	}
	defer func() { <-t.sendGate }()
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
		t.conn.Gateway().(*voiceSocket).shutdown()
		<-t.eventsDone
		t.sendGate <- struct{}{} // Track actual sender completion before cleanup.
		defer func() { <-t.sendGate }()
		t.cleanup()
	})
}
func (t *discordVoiceTransport) Write(ctx context.Context, p []byte) error {
	if err := t.acquireSender(ctx); err != nil {
		return err
	}
	defer func() { <-t.sendGate }()
	if !t.dave.Ready() {
		return errDaveNotReady
	}
	t.mu.Lock()
	speaking, cause := t.speaking, t.traceCause
	t.mu.Unlock()
	if !speaking {
		opts := []trace.SpanStartOption{}
		if cause.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: cause}))
		}
		speakCtx, span := tracerOrNoop(t.tracer).Start(ctx, "voice.speaking", opts...)
		err := t.conn.SetSpeaking(speakCtx, voice.SpeakingFlagMicrophone)
		finishTrace(span, err)
		if err != nil {
			return err
		}
		t.mu.Lock()
		t.speaking = true
		t.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.invalid.Load() {
		return context.Canceled
	}
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = t.conn.UDP().SetWriteDeadline(deadline)
	_, err := t.conn.UDP().Write(p)
	return err
}
func (t *discordVoiceTransport) Finish(ctx context.Context) {
	if err := t.acquireSender(ctx); err != nil {
		return
	}
	defer func() { <-t.sendGate }()
	t.mu.Lock()
	speaking := t.speaking
	t.mu.Unlock()
	if !speaking {
		return
	}
	_ = t.conn.UDP().SetWriteDeadline(time.Now().Add(time.Second))
	for range 5 {
		if t.invalid.Load() || ctx.Err() != nil {
			return
		}
		if _, err := t.conn.UDP().Write(voice.SilenceAudioFrame); err != nil {
			break
		}
	}
	_ = t.conn.SetSpeaking(ctx, voice.SpeakingFlagNone)
	t.mu.Lock()
	t.speaking = false
	t.mu.Unlock()
}

func (a *App) openVoiceTransport(ctx context.Context, state voice.State, closed func(error, bool), opts []voice.ConnConfigOpt) (voiceTransport, error) {
	return a.openVoiceTransportWithDialer(ctx, state, closed, opts, websocket.DefaultDialer)
}

func (a *App) openVoiceTransportWithDialer(ctx context.Context, state voice.State, closed func(error, bool), opts []voice.ConnConfigOpt, dialer *websocket.Dialer) (voiceTransport, error) {
	ctx, cancelHandshake := context.WithTimeout(ctx, voiceRecoveryTimeout)
	defer cancelHandshake()
	logger := logging.Component(a.logger, "voice").With("guild_id", state.GuildID.String())
	t := &discordVoiceTransport{state: state, tracer: a.tracer, traceCause: trace.SpanContextFromContext(ctx), sendGate: make(chan struct{}, 1)}
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	t.cancelEvents, t.eventsDone = cancelEvents, make(chan struct{})
	t.done = eventCtx.Done()
	// This worker survives successful establishment and Resume. In particular,
	// SessionDescription and subsequent DAVE events share one ordered queue.
	type transportEvent struct {
		observation context.Context
		gateway     voice.Gateway
		op          voice.Opcode
		seq         int
		data        voice.GatewayMessageData
	}
	events := make(chan transportEvent, voiceEventCapacity)
	var handle voiceSocketEventHandler
	described := make(chan error, 1)
	failed := make(chan error, 1)
	var established atomic.Bool
	var failOnce sync.Once
	var daveDiagnostic atomic.Value
	var deadlines *voiceHandshakeDeadline
	failHandshake := func(stage string, err error) {
		failOnce.Do(func() {
			deadlines.stop()
			failed <- &voiceStageError{stage, err}
		})
	}
	overall, _ := ctx.Deadline()
	progressRecord, _ := ctx.Value(voiceTraceKey{}).(*voiceTraceAttempt)
	deadlines = &voiceHandshakeDeadline{record: progressRecord, overall: overall, fail: func(name string, err error) {
		failHandshake(name, err)
		cancelHandshake()
	}}
	defer deadlines.stop()
	progress := func(observation context.Context, name string) {
		if established.Load() {
			traceVoiceStage(observation, name)
		} else {
			deadlines.advance(name)
		}
	}
	failure := func(err error) error {
		select {
		case original := <-failed:
			err = original
		default:
		}
		var detail *voiceStageError
		if !errors.As(err, &detail) {
			detail = &voiceStageError{deadlines.current(), err}
			err = detail
		}
		attrs := []any{"event", "voice.handshake.failed", "stage", detail.stage, "reason", failureClass(err)}
		attrs = append(attrs, deadlines.progressRecord().diagnostic()...)
		if snapshot := daveDiagnostic.Load(); snapshot != nil {
			attrs = append(attrs, snapshot.([]any)...)
		}
		recordVoiceDiagnostic(ctx, logger, slog.LevelDebug, "voice handshake failed", attrs...)
		disposeVoiceTransport(ctx, t)
		return err
	}
	options := []voice.ConnConfigOpt{voice.WithUDPConnCreateFunc(func(dave godave.Session, _ voice.SsrcLookupFunc, _ ...voice.UDPConnConfigOpt) voice.UDPConn {
		return &voiceUDP{dave: dave}
	})}
	options = append(options, opts...)
	options = append(options, voice.WithConnGatewayCreateFunc(func(dave godave.Session, event voice.EventHandlerFunc, _ voice.CloseHandlerFunc, opts ...voice.GatewayConfigOpt) voice.Gateway {
		t.dave = dave
		dave.SetChannelID(godave.ChannelID(*state.ChannelID))
		var description *voice.GatewayMessageDataSessionDescription // Ordered worker only.
		protocolSelected := false                                   // Ordered worker only; never accept keys before selection.
		handle = func(observation context.Context, g voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
			if t.invalid.Load() {
				return
			}
			traceVoiceEvent(observation, int(op), true)
			defer traceVoiceEvent(observation, int(op), false)
			if op == voice.OpcodeSessionDescription || int(op) >= 21 {
				defer func() {
					attrs := []any{"event", "voice.dave.event", "opcode", int(op), "sequence", seq}
					if diagnostic, ok := dave.(interface{ VoiceDiagnostic() []any }); ok {
						snapshot := diagnostic.VoiceDiagnostic()
						daveDiagnostic.Store(snapshot)
						attrs = append(attrs, snapshot...)
					}
					recordVoiceDiagnostic(observation, logger, slog.LevelDebug, "voice DAVE event processed", attrs...)
				}()
			}
			if d, ok := data.(voice.GatewayMessageDataReady); ok {
				progress(observation, "udp_discovery")
				discoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				stop := context.AfterFunc(eventCtx, cancel)
				address, port, err := t.conn.UDP().Open(discoveryCtx, d.IP, d.Port, d.SSRC)
				stop()
				cancel()
				if err != nil {
					failHandshake("udp_discovery", err)
					return
				}
				progress(observation, "select_protocol")
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
				protocolSelected = true
				progress(observation, "session_description")
				return
			}
			// We own readiness, so do not use Conn.Open's completion channel.
			if d, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
				if !protocolSelected {
					failHandshake("session_description", errors.New("voice session description before protocol selection"))
					t.Invalidate()
					return
				}
				if description != nil {
					if d.Mode == description.Mode && d.DaveProtocolVersion == description.DaveProtocolVersion && bytes.Equal(d.SecretKey, description.SecretKey) {
						recordVoiceDiagnostic(observation, logger, slog.LevelDebug, "duplicate voice description ignored", "event", "voice.protocol.duplicate", "opcode", int(op))
						return
					}
					err := &voiceStageError{"session_description", errors.New("conflicting voice session description")}
					failHandshake("session_description", err)
					cancelHandshake()
					t.Invalidate()
					if established.Load() {
						closed(err, false)
					}
					return
				}
				copy := d
				copy.SecretKey = bytes.Clone(d.SecretKey)
				description = &copy
				progress(observation, "transport_key")
				err := t.conn.UDP().SetSecretKey(d.Mode, d.SecretKey)
				if err != nil {
					failHandshake("transport_key", err)
					if established.Load() {
						closed(err, false)
					}
					return
				}
				progress(observation, "dave")
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
		socket := newVoiceSocket(dave, func(observation context.Context, g voice.Gateway, op voice.Opcode, seq int, data voice.GatewayMessageData) {
			if t.invalid.Load() {
				return
			}
			select {
			case events <- transportEvent{observation, g, op, seq, data}:
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
				failHandshake(deadlines.current(), err)
				if established.Load() {
					closed(err, reconnect)
				}
			}
		}, dialer)
		socket.progress = progress
		socket.valid = func() bool { return !t.invalid.Load() }
		var daveFailureOnce sync.Once
		socket.daveFailed = func(err error) {
			daveFailureOnce.Do(func() {
				if t.invalid.Load() {
					return
				}
				t.Invalidate()
				failHandshake("dave_send", err)
				if established.Load() {
					closed(err, false)
				}
			})
		}
		return socket
	}))
	// Reuse disgo's UDP and event types, but own discovery/protocol errors.
	t.conn = voice.NewConn(state.GuildID, state.UserID, func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }, func() {}, options...)
	t.cleanup = a.takeDaveCleanup(t.conn)
	go func() {
		defer close(t.eventsDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		described := false
		for {
			select {
			case <-eventCtx.Done():
				return
			case <-ticker.C:
				if described {
					t.daveReady.Store(t.dave.Ready())
				}
			case e := <-events:
				if eventCtx.Err() != nil {
					return
				}
				changesDave := e.op == voice.OpcodeSessionDescription || int(e.op) >= 21
				if changesDave {
					t.daveReady.Store(false)
				}
				handle(e.observation, e.gateway, e.op, e.seq, e.data)
				if e.op == voice.OpcodeSessionDescription {
					described = true
				}
				if changesDave && described {
					t.daveReady.Store(t.dave.Ready())
				}
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
	for !t.daveReady.Load() {
		select {
		case <-ctx.Done():
			return nil, failure(ctx.Err())
		case err := <-failed:
			return nil, failure(err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !deadlines.finish() {
		return nil, failure(context.DeadlineExceeded)
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
