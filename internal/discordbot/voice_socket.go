package discordbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/gorilla/websocket"
)

// voiceSocket implements only the wire protocol. It never rejoins a VC or
// reconnects itself: the connection controller owns those decisions. Every
// reader and heartbeat belongs to a particular run, including across Resume.
type voiceSocketEventHandler func(context.Context, voice.Gateway, voice.Opcode, int, voice.GatewayMessageData)

// disgo v0.19.6 tags SeqAck as "seq"; Voice Gateway v8 requires "seq_ack".
// Embed its data type to preserve the library's sealed message interface.
type voiceResumeData struct {
	voice.GatewayMessageDataResume
	SeqAck int
}

func (d voiceResumeData) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"server_id": d.GuildID, "session_id": d.SessionID,
		"token": d.Token, "seq_ack": d.SeqAck,
	})
}

type voiceSocket struct {
	mu          sync.Mutex
	run         *voiceSocketRun
	ssrc        uint32
	seq         int
	status      voice.Status
	latency     time.Duration
	state       voice.State
	established bool
	dave        godave.Session
	event       voiceSocketEventHandler
	closed      voice.CloseHandlerFunc
	dialer      *websocket.Dialer
	progress    func(context.Context, string)
	disposed    bool
	workers     sync.WaitGroup
	valid       func() bool
	daveFailed  func(error)
}

type voiceSocketRun struct {
	observation      context.Context
	ctx              context.Context
	cancel           context.CancelFunc
	ws               *websocket.Conn
	writeGate        chan struct{}
	ready            chan error
	readyOnce        sync.Once
	heartbeatStarted bool // protected by voiceSocket.mu
	resume           bool
	awaiting         bool
	nonce            int64
	sent             time.Time
}

func (r *voiceSocketRun) complete(err error) { r.readyOnce.Do(func() { r.ready <- err }) }

func newVoiceSocket(dave godave.Session, event voiceSocketEventHandler, closed voice.CloseHandlerFunc, dialer *websocket.Dialer) *voiceSocket {
	return &voiceSocket{dave: dave, event: event, closed: closed, dialer: dialer, seq: -1}
}

func (g *voiceSocket) SSRC() uint32           { g.mu.Lock(); defer g.mu.Unlock(); return g.ssrc }
func (g *voiceSocket) Status() voice.Status   { g.mu.Lock(); defer g.mu.Unlock(); return g.status }
func (g *voiceSocket) Latency() time.Duration { g.mu.Lock(); defer g.mu.Unlock(); return g.latency }

func (g *voiceSocket) stage(ctx context.Context, name string) {
	if g.progress != nil {
		g.progress(ctx, name)
	} else {
		traceVoiceStage(ctx, name)
	}
}

func (g *voiceSocket) Open(ctx context.Context, state voice.State) error {
	return g.open(ctx, state, false)
}

func (g *voiceSocket) Resume(ctx context.Context, state voice.State) error {
	return g.open(ctx, state, true)
}

func (g *voiceSocket) open(ctx context.Context, state voice.State, resume bool) error {
	g.mu.Lock()
	if g.disposed {
		g.mu.Unlock()
		return context.Canceled
	}
	if g.run != nil {
		g.mu.Unlock()
		return voice.ErrGatewayAlreadyConnected
	}
	if resume && !g.established {
		g.mu.Unlock()
		return errors.New("voice session cannot resume before establishment")
	}
	if !resume {
		g.ssrc, g.seq, g.established = 0, -1, false
	}
	rctx, cancel := context.WithCancel(context.Background())
	r := &voiceSocketRun{ctx: rctx, cancel: cancel, ready: make(chan error, 1), resume: resume, writeGate: make(chan struct{}, 1)}
	r.observation = ctx
	g.run, g.state, g.status = r, state, voice.StatusConnecting
	g.workers.Add(1)
	g.mu.Unlock()
	defer g.workers.Done()
	// Cancellation of either Open or Close must also cancel an in-flight dial.
	dialCtx, dialCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(rctx, dialCancel)
	g.stage(ctx, "websocket_connect")
	ws, response, err := g.dialer.DialContext(dialCtx, fmt.Sprintf("wss://%s?v=%d", state.Endpoint, voice.GatewayVersion), nil)
	stop()
	dialCancel()
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		g.finish(r, err, false)
		return err
	}
	ws.SetReadLimit(4 << 20)
	ws.SetCloseHandler(func(int, string) error { return nil })
	g.mu.Lock()
	if g.run != r || r.ctx.Err() != nil {
		g.mu.Unlock()
		_ = ws.Close()
		return context.Canceled
	}
	r.ws = ws
	g.status = voice.StatusWaitingForHello
	g.workers.Add(1)
	g.mu.Unlock()
	g.stage(ctx, "hello")
	go func() { defer g.workers.Done(); g.read(r) }()
	select {
	case err := <-r.ready:
		return err
	case <-ctx.Done():
		g.finish(r, ctx.Err(), false)
		return ctx.Err()
	}
}

// finish invalidates the run before closing its socket. A stale reader can
// neither close a replacement socket nor publish a replacement's close event.
func (g *voiceSocket) finish(r *voiceSocketRun, err error, notify bool) {
	g.mu.Lock()
	if g.run != r {
		g.mu.Unlock()
		return
	}
	g.run = nil
	g.status = voice.StatusDisconnected
	// The run context stops reader/heartbeat work. Open/Resume receives the
	// actual terminal cause through ready, never through this internal cancel.
	r.cancel()
	ws := r.ws
	g.mu.Unlock()
	r.complete(err)
	if ws != nil {
		_ = ws.Close()
	}
	if notify && g.closed != nil {
		reconnect := true
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			reconnect = voice.GatewayCloseEventCodeByCode(ce.Code).Reconnect
		}
		g.closed(g, err, reconnect)
	}
}

func (g *voiceSocket) Close() { g.CloseWithCode(websocket.CloseNormalClosure, "Shutting down") }

func (g *voiceSocket) shutdown() {
	g.mu.Lock()
	g.disposed = true
	g.mu.Unlock()
	g.Close()
	g.workers.Wait()
}
func (g *voiceSocket) CloseWithCode(code int, reason string) {
	g.mu.Lock()
	r := g.run
	if code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway {
		g.ssrc, g.seq, g.established = 0, -1, false
	}
	g.mu.Unlock()
	if r != nil {
		g.finish(r, context.Canceled, false)
	}
}

func (g *voiceSocket) Send(ctx context.Context, op voice.Opcode, data voice.GatewayMessageData) error {
	if g.valid != nil && !g.valid() {
		return context.Canceled
	}
	g.mu.Lock()
	r, ready := g.run, g.status == voice.StatusReady
	g.mu.Unlock()
	if r == nil || !ready {
		return g.sendFailure(r, op, voice.ErrGatewayNotConnected)
	}
	err := g.send(ctx, r, op, data)
	if err != nil && isDaveSend(op) {
		return g.sendFailure(r, op, err)
	}
	// DAVE callbacks do not return their errors to the handshake caller.
	// A failed wire send must also reach the connection controller.
	if err != nil && ctx.Err() == nil && r.ctx.Err() == nil {
		g.finish(r, err, true)
	}
	return err
}

var errDaveTransportUnsafe = errors.New("DAVE send failed; session must be replaced")

type daveSendError struct{ cause error }

func (e *daveSendError) Error() string        { return errDaveTransportUnsafe.Error() }
func (e *daveSendError) Unwrap() error        { return e.cause }
func (e *daveSendError) Is(target error) bool { return target == errDaveTransportUnsafe }

func isDaveSend(op voice.Opcode) bool {
	return op == voice.OpcodeDaveMLSKeyPackage || op == voice.OpcodeDaveMLSCommitWelcome || op == voice.OpcodeDaveTransitionReady || op == voice.OpcodeDaveMLSInvalidCommitWelcome
}

func (g *voiceSocket) sendFailure(r *voiceSocketRun, op voice.Opcode, err error) error {
	if !isDaveSend(op) || g.daveFailed == nil {
		return err
	}
	if g.valid != nil && !g.valid() {
		return context.Canceled
	}
	// Retaining this MLS state after a failed callback is not supported. In
	// particular, do not let dependency backoff race the connection supervisor.
	failure := &daveSendError{cause: err}
	if r != nil {
		g.finish(r, failure, false)
	}
	g.daveFailed(failure)
	return failure
}

func (g *voiceSocket) send(ctx context.Context, r *voiceSocketRun, op voice.Opcode, data voice.GatewayMessageData) error {
	// The operation budget includes waiting for another writer, not just I/O.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var payload []byte
	var err error
	messageType := websocket.TextMessage
	if op.IsBinary() {
		// All outgoing DAVE binary message bodies are byte slices.
		switch d := data.(type) {
		case voice.GatewayMessageDataDaveMLSKeyPackage:
			payload = append([]byte{byte(op)}, d...)
		case voice.GatewayMessageDataDaveMLSCommitWelcome:
			payload = append([]byte{byte(op)}, d...)
		default:
			return fmt.Errorf("unsupported outgoing voice binary opcode %d", op)
		}
		messageType = websocket.BinaryMessage
	} else {
		payload, err = json.Marshal(voice.GatewayMessage{Op: op, D: data})
	}
	if err != nil {
		return err
	}
	select {
	case r.writeGate <- struct{}{}:
		defer func() { <-r.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	_ = r.ws.SetWriteDeadline(deadline)
	return r.ws.WriteMessage(messageType, payload)
}

func (g *voiceSocket) heartbeat(r *voiceSocketRun, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		g.mu.Lock()
		if g.run != r {
			g.mu.Unlock()
			return
		}
		if r.awaiting {
			g.mu.Unlock()
			g.finish(r, errors.New("voice heartbeat acknowledgement timed out"), true)
			return
		}
		r.nonce, r.sent, r.awaiting = time.Now().UnixMilli(), time.Now(), true
		data := voice.GatewayMessageDataHeartbeat{T: r.nonce, SeqAck: g.seq}
		g.mu.Unlock()
		if err := g.send(r.ctx, r, voice.OpcodeHeartbeat, data); err != nil {
			g.finish(r, err, true)
			return
		}
	}
}

func (g *voiceSocket) read(r *voiceSocketRun) {
	for {
		kind, payload, err := r.ws.ReadMessage()
		if err != nil {
			g.finish(r, err, true)
			return
		}
		var message voice.GatewayMessage
		if kind == websocket.BinaryMessage {
			err = message.UnmarshalBinary(bytes.NewReader(payload))
		} else {
			err = json.Unmarshal(payload, &message)
		}
		if err != nil {
			g.finish(r, fmt.Errorf("decode voice message: %w", err), true)
			return
		}
		g.mu.Lock()
		if g.run != r {
			g.mu.Unlock()
			return
		}
		if seq, present := voiceSequence(kind, payload); present {
			g.seq = seq
		}
		g.mu.Unlock()
		switch message.Op {
		case voice.OpcodeHello:
			d := message.D.(voice.GatewayMessageDataHello)
			interval := time.Duration(d.HeartbeatInterval) * time.Millisecond
			if interval <= 0 {
				g.finish(r, errors.New("invalid voice heartbeat interval"), true)
				return
			}
			g.mu.Lock()
			if g.run != r {
				g.mu.Unlock()
				return
			}
			if r.heartbeatStarted {
				g.mu.Unlock()
				continue
			}
			r.heartbeatStarted = true
			state, seq := g.state, g.seq
			resume := r.resume
			g.workers.Add(1)
			g.mu.Unlock()
			go func() { defer g.workers.Done(); g.heartbeat(r, interval) }()
			if resume {
				g.stage(r.observation, "resume")
				err = g.send(r.ctx, r, voice.OpcodeResume, voiceResumeData{GatewayMessageDataResume: voice.GatewayMessageDataResume{GuildID: state.GuildID, SessionID: state.SessionID, Token: state.Token}, SeqAck: seq})
			} else {
				g.stage(r.observation, "identify")
				err = g.send(r.ctx, r, voice.OpcodeIdentify, voice.GatewayMessageDataIdentify{GuildID: state.GuildID, UserID: state.UserID, SessionID: state.SessionID, Token: state.Token, MaxDaveProtocolVersion: g.dave.MaxSupportedProtocolVersion()})
			}
			if err != nil {
				g.finish(r, err, true)
				return
			}
			if !resume {
				g.stage(r.observation, "ready")
			}
		case voice.OpcodeReady, voice.OpcodeResumed:
			g.mu.Lock()
			if g.run != r {
				g.mu.Unlock()
				return
			}
			if !r.heartbeatStarted || (message.Op == voice.OpcodeResumed) != r.resume {
				g.mu.Unlock()
				g.finish(r, errors.New("unexpected voice establishment event"), true)
				return
			}
			// Repeated establishment events must not repeat UDP discovery or
			// reset encryption state on an already established socket.
			if g.status == voice.StatusReady {
				g.mu.Unlock()
				continue
			}
			if d, ok := message.D.(voice.GatewayMessageDataReady); ok {
				g.ssrc = d.SSRC
			}
			g.status = voice.StatusReady
			g.established = true
			g.mu.Unlock()
			r.complete(nil)
		case voice.OpcodeHeartbeatACK:
			d := message.D.(voice.GatewayMessageDataHeartbeatACK)
			g.mu.Lock()
			if g.run == r && r.awaiting && d.T == r.nonce {
				r.awaiting = false
				g.latency = time.Since(r.sent)
			}
			g.mu.Unlock()
		}
		// The transport serializes DAVE/UDP events with teardown and audio.
		if r.ctx.Err() == nil {
			traceVoiceReceived(r.observation, int(message.Op))
			g.event(r.observation, g, message.Op, message.Seq, message.D)
		}
	}
}

// The dependency's int field loses the distinction between absent and zero.
// Preserve zero after sequence wrap; binary messages always carry a sequence.
func voiceSequence(kind int, payload []byte) (int, bool) {
	if kind == websocket.BinaryMessage {
		if len(payload) < 2 {
			return 0, false
		}
		return int(payload[0])<<8 | int(payload[1]), true
	}
	var envelope struct {
		S   *int `json:"s"`
		Seq *int `json:"seq"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return 0, false
	}
	if envelope.Seq != nil {
		return *envelope.Seq, true
	}
	if envelope.S != nil {
		return *envelope.S, true
	}
	return 0, false
}
