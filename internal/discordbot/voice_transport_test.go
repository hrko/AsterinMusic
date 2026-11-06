package discordbot

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

type protocolTestUDP struct {
	voice.UDPConn
	written atomic.Int32
	opened  atomic.Int32
	closed  atomic.Bool
}

type blockedProtocolUDP struct {
	protocolTestUDP
	entered chan struct{}
	release chan struct{}
}

type orderedProtocolDave struct {
	godave.Session
	entered, release, handled chan struct{}
	initialized               atomic.Bool
	overtaken                 atomic.Bool
}

func (d *orderedProtocolDave) OnSelectProtocolAck(uint16) {
	close(d.entered)
	<-d.release
	d.initialized.Store(true)
}
func (d *orderedProtocolDave) OnDavePrepareEpoch(int, uint16) {
	d.overtaken.Store(!d.initialized.Load())
	close(d.handled)
}

func TestVoiceDaveEventsCannotOvertakeSessionDescription(t *testing.T) {
	d := &orderedProtocolDave{entered: make(chan struct{}), release: make(chan struct{}), handled: make(chan struct{})}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 60000}})
		for {
			var message struct {
				Op int `json:"op"`
			}
			if ws.ReadJSON(&message) != nil {
				return
			}
			switch message.Op {
			case 0:
				_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
			case 1:
				_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": make([]int, 32), "dave_protocol_version": 0}})
				_ = ws.WriteJSON(map[string]any{"op": int(voice.OpcodeDavePrepareEpoch), "d": map[string]any{"epoch": 2, "protocol_version": 1}})
			}
		}
	}))
	defer server.Close()
	defer func() {
		select {
		case <-d.release:
		default:
			close(d.release)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a := &App{}
	channel := snowflake.ID(3)
	type result struct {
		transport voiceTransport
		err       error
	}
	completed := make(chan result, 1)
	go func() {
		transport, err := a.openVoiceTransportWithDialer(ctx, voice.State{ChannelID: &channel, Endpoint: strings.TrimPrefix(server.URL, "https://")}, func(error, bool) {}, []voice.ConnConfigOpt{
			voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn {
				return &protocolTestUDP{}
			}),
			voice.WithConnDaveSessionCreateFunc(func(logger *slog.Logger, user godave.UserID, cb godave.Callbacks) godave.Session {
				d.Session = godave.NewNoopSession(logger, user, cb)
				return d
			}),
		}, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
		completed <- result{transport, err}
	}()
	select {
	case <-d.entered:
	case <-ctx.Done():
		t.Fatal("initialization not entered")
	}
	select {
	case <-d.handled:
		t.Fatal("DAVE event overtook initialization")
	default:
	}
	close(d.release)
	select {
	case <-d.handled:
	case <-ctx.Done():
		t.Fatal("DAVE event never processed")
	}
	if d.overtaken.Load() {
		t.Fatal("DAVE event observed uninitialized state")
	}
	got := <-completed
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.transport.Close()
}

func TestVoiceTransportEventOverflowFailsAndTracksBlockedWorker(t *testing.T) {
	u := &blockedProtocolUDP{entered: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 60000}})
		var identify map[string]any
		if ws.ReadJSON(&identify) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
		select {
		case <-u.entered:
		case <-time.After(time.Second):
			return
		}
		for range voiceEventCapacity + 8 {
			if ws.WriteJSON(map[string]any{"op": 6, "d": map[string]int{"t": 0}}) != nil {
				return
			}
		}
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var resources voiceResources
	lease := resources.acquire()
	ctx = context.WithValue(ctx, voiceResourceKey{}, lease)
	defer func() {
		close(u.release)
		select {
		case <-lease.done:
		case <-time.After(time.Second):
			t.Error("overflowed transport worker leaked")
		}
	}()
	channel := snowflake.ID(3)
	a := &App{}
	transport, err := a.openVoiceTransportWithDialer(ctx, voice.State{ChannelID: &channel, Endpoint: strings.TrimPrefix(server.URL, "https://")}, func(error, bool) {}, []voice.ConnConfigOpt{
		voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn { return u }),
	}, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
	if transport != nil {
		lease.retire(transport)
		t.Fatal("queue overflow accepted connection")
	}
	var failure *voiceStageError
	if !errors.As(err, &failure) || failure.stage != "event_queue" {
		t.Fatalf("overflow did not terminate at event queue: %v", err)
	}
	select {
	case <-lease.done:
		t.Fatal("blocked worker counted as cleaned up")
	default:
	}
}

func (u *blockedProtocolUDP) Open(context.Context, string, int, uint32) (string, int, error) {
	close(u.entered)
	<-u.release
	return "127.0.0.1", 9000, nil
}

func TestVoiceTransportHeartbeatDuringDiscovery(t *testing.T) {
	u := &blockedProtocolUDP{entered: make(chan struct{}), release: make(chan struct{})}
	heartbeats := make(chan struct{}, 16)
	closed := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 50}})
		for {
			var m struct {
				Op int             `json:"op"`
				D  json.RawMessage `json:"d"`
			}
			if ws.ReadJSON(&m) != nil {
				return
			}
			switch m.Op {
			case 0:
				_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
			case 3:
				_ = ws.WriteJSON(map[string]any{"op": 6, "d": m.D})
				select {
				case heartbeats <- struct{}{}:
				default:
				}
			case 1:
				_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": make([]int, 32), "dave_protocol_version": 0}})
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tracer := tracerOrNoop(nil)
	ctx, span := tracer.Start(ctx, "voice.connect")
	attempt := &voiceTraceAttempt{ctx: ctx, span: span, tracer: tracer}
	ctx = context.WithValue(ctx, voiceTraceKey{}, attempt)
	result := make(chan error, 1)
	a := &App{}
	channel := snowflake.ID(3)
	go func() {
		tr, err := a.openVoiceTransportWithDialer(ctx, voice.State{ChannelID: &channel, Endpoint: strings.TrimPrefix(server.URL, "https://")}, func(err error, _ bool) {
			select {
			case closed <- err:
			default:
			}
		}, []voice.ConnConfigOpt{
			voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn { return u }),
		}, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
		if err == nil {
			tr.Close()
		}
		result <- err
	}()
	// Release discovery even on failure, so the test never leaks its worker.
	defer func() {
		close(u.release)
		if err := <-result; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	select {
	case <-u.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 4 {
		select {
		case <-heartbeats:
		case err := <-closed:
			t.Fatalf("discovery blocked heartbeat ACK: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	attempt.mu.Lock()
	active, opcode, stage := attempt.handling, attempt.handlingOpcode, attempt.name
	attempt.mu.Unlock()
	if !active || opcode != int(voice.OpcodeReady) || stage != "udp_discovery" {
		t.Fatalf("blocked worker hidden by heartbeat/enqueue activity: active=%v opcode=%d stage=%s", active, opcode, stage)
	}
}

func (u *protocolTestUDP) Open(context.Context, string, int, uint32) (string, int, error) {
	u.opened.Add(1)
	return "127.0.0.1", 9000, nil
}
func (u *protocolTestUDP) SetSecretKey(voice.EncryptionMode, []byte) error { return nil }
func (u *protocolTestUDP) SetWriteDeadline(time.Time) error                { return nil }
func (u *protocolTestUDP) Write(p []byte) (int, error)                     { u.written.Add(1); return len(p), nil }
func (u *protocolTestUDP) Close() error                                    { u.closed.Store(true); return nil }

// Exercise the actual disgo handshake and completion channel, with only the
// remote gateway, UDP network and crypto replaced by local test fixtures.
func TestVoiceTransportHandshakeAndFreshResources(t *testing.T) {
	speaking := make(chan struct{}, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]any{"heartbeat_interval": 60000}})
		for {
			var message struct {
				Op int             `json:"op"`
				D  json.RawMessage `json:"d"`
			}
			if err := ws.ReadJSON(&message); err != nil {
				return
			}
			switch message.Op {
			case 7:
				_ = ws.WriteJSON(map[string]any{"op": 9, "d": nil})
			case 0:
				_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
				_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
			case 1:
				_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": make([]int, 32), "dave_protocol_version": 0}})
			case 5:
				speaking <- struct{}{}
			}
		}
	}))
	defer server.Close()
	a := &App{}
	var udp []*protocolTestUDP
	var sessions atomic.Int32
	opts := []voice.ConnConfigOpt{
		voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn {
			u := &protocolTestUDP{}
			udp = append(udp, u)
			return u
		}),
		voice.WithConnDaveSessionCreateFunc(func(logger *slog.Logger, user godave.UserID, cb godave.Callbacks) godave.Session {
			sessions.Add(1)
			s := godave.NewNoopSession(logger, user, cb)
			a.daveClosers.Store(cb, s)
			return s
		}),
	}
	for _, channel := range []snowflake.ID{3, 4, 3} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		transport, err := a.openVoiceTransportWithDialer(ctx, voice.State{GuildID: 1, UserID: 2, ChannelID: &channel, SessionID: "test", Token: "test", Endpoint: strings.TrimPrefix(server.URL, "https://")}, func(error, bool) {}, opts, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}) // local test server only
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = transport.Write(ctx, []byte{1}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-speaking:
		case <-ctx.Done():
			t.Fatal("speaking not sent")
		}
		live := transport.(*discordVoiceTransport)
		live.conn.Gateway().CloseWithCode(4015, "local test")
		if err := live.Resume(ctx); err != nil {
			t.Fatal(err)
		}
		if err := live.Write(ctx, []byte{2}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-speaking:
		case <-ctx.Done():
			t.Fatal("Resume did not reannounce speaking")
		}
		transport.Invalidate()
		if err = transport.Write(ctx, []byte{2}); err == nil {
			t.Fatal("retired transport admitted audio")
		}
		transport.Close()
		cancel()
	}
	if sessions.Load() != 3 || len(udp) != 3 {
		t.Fatal("reused per-channel resources")
	}
	for _, u := range udp {
		if !u.closed.Load() || u.written.Load() != 2 || u.opened.Load() != 1 {
			t.Fatal("teardown or send failed")
		}
	}
	a.daveClosers.Range(func(any, any) bool { t.Error("DAVE resource leaked"); return true })
}
