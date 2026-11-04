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
type voiceSocket struct {
	mu      sync.Mutex
	run     *voiceSocketRun
	ssrc    uint32
	seq     int
	status  voice.Status
	latency time.Duration
	state   voice.State
	dave    godave.Session
	event   voice.EventHandlerFunc
	closed  voice.CloseHandlerFunc
	dialer  *websocket.Dialer
}

type voiceSocketRun struct {
	ctx              context.Context
	cancel           context.CancelFunc
	ws               *websocket.Conn
	writeMu          sync.Mutex
	ready            chan error
	readyOnce        sync.Once
	heartbeatStarted bool // protected by voiceSocket.mu
	awaiting         bool
	nonce            int64
	sent             time.Time
}

func (r *voiceSocketRun) complete(err error) { r.readyOnce.Do(func() { r.ready <- err }) }

func newVoiceSocket(dave godave.Session, event voice.EventHandlerFunc, closed voice.CloseHandlerFunc, dialer *websocket.Dialer) *voiceSocket {
	return &voiceSocket{dave: dave, event: event, closed: closed, dialer: dialer}
}

func (g *voiceSocket) SSRC() uint32           { g.mu.Lock(); defer g.mu.Unlock(); return g.ssrc }
func (g *voiceSocket) Status() voice.Status   { g.mu.Lock(); defer g.mu.Unlock(); return g.status }
func (g *voiceSocket) Latency() time.Duration { g.mu.Lock(); defer g.mu.Unlock(); return g.latency }

func (g *voiceSocket) Open(ctx context.Context, state voice.State) error {
	g.mu.Lock()
	if g.run != nil {
		g.mu.Unlock()
		return voice.ErrGatewayAlreadyConnected
	}
	rctx, cancel := context.WithCancel(context.Background())
	r := &voiceSocketRun{ctx: rctx, cancel: cancel, ready: make(chan error, 1)}
	g.run, g.state, g.status = r, state, voice.StatusConnecting
	g.mu.Unlock()
	// Cancellation of either Open or Close must also cancel an in-flight dial.
	dialCtx, dialCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(rctx, dialCancel)
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
	g.mu.Unlock()
	go g.read(r)
	select {
	case err := <-r.ready:
		return err
	case <-ctx.Done():
		g.finish(r, ctx.Err(), false)
		return ctx.Err()
	case <-r.ctx.Done():
		return context.Canceled
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
func (g *voiceSocket) CloseWithCode(code int, reason string) {
	g.mu.Lock()
	r := g.run
	if code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway {
		g.ssrc, g.seq = 0, 0
	}
	g.mu.Unlock()
	if r != nil {
		g.finish(r, context.Canceled, false)
	}
}

func (g *voiceSocket) Send(ctx context.Context, op voice.Opcode, data voice.GatewayMessageData) error {
	g.mu.Lock()
	r, ready := g.run, g.status == voice.StatusReady
	g.mu.Unlock()
	if r == nil || !ready {
		return voice.ErrGatewayNotConnected
	}
	return g.send(ctx, r, op, data)
}

func (g *voiceSocket) send(ctx context.Context, r *voiceSocketRun, op voice.Opcode, data voice.GatewayMessageData) error {
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
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
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
		if message.Seq > 0 {
			g.seq = message.Seq
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
			resume := g.ssrc != 0 && seq != 0
			g.mu.Unlock()
			go g.heartbeat(r, interval)
			if resume {
				err = g.send(r.ctx, r, voice.OpcodeResume, voice.GatewayMessageDataResume{GuildID: state.GuildID, SessionID: state.SessionID, Token: state.Token, SeqAck: seq})
			} else {
				err = g.send(r.ctx, r, voice.OpcodeIdentify, voice.GatewayMessageDataIdentify{GuildID: state.GuildID, UserID: state.UserID, SessionID: state.SessionID, Token: state.Token, MaxDaveProtocolVersion: g.dave.MaxSupportedProtocolVersion()})
			}
			if err != nil {
				g.finish(r, err, true)
				return
			}
		case voice.OpcodeReady, voice.OpcodeResumed:
			g.mu.Lock()
			if g.run != r {
				g.mu.Unlock()
				return
			}
			if d, ok := message.D.(voice.GatewayMessageDataReady); ok {
				g.ssrc = d.SSRC
			}
			g.status = voice.StatusReady
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
			g.event(g, message.Op, message.Seq, message.D)
		}
	}
}
