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

func TestVoiceSocketWaitingWriterCanBeCanceled(t *testing.T) {
	for _, mode := range []string{"caller", "run", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			runCtx, stopRun := context.WithCancel(context.Background())
			defer stopRun()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			r := &voiceSocketRun{ctx: runCtx, writeGate: make(chan struct{}, 1)}
			r.writeGate <- struct{}{} // A previous writer owns the wire.
			done := make(chan error, 1)
			go func() {
				done <- (&voiceSocket{}).send(ctx, r, voice.OpcodeHeartbeat, voice.GatewayMessageDataHeartbeat{})
			}()
			want := context.Canceled
			switch mode {
			case "caller":
				cancel()
			case "run":
				stopRun()
			case "deadline":
				want = context.DeadlineExceeded
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("waiting writer: %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled writer still waits for previous writer")
			}
			if len(r.writeGate) != 1 {
				t.Fatal("canceled waiter released another writer's ownership")
			}
		})
	}
}

func TestVoiceSocketHeartbeatResumeAndBinary(t *testing.T) {
	var connections atomic.Int32
	ops := make(chan int, 4)
	binary := make(chan []byte, 1)
	resumePayload := make(chan json.RawMessage, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		n := connections.Add(1)
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 50}})
		for {
			kind, p, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.BinaryMessage {
				binary <- p
				continue
			}
			var m struct {
				Op int             `json:"op"`
				D  json.RawMessage `json:"d"`
			}
			_ = json.Unmarshal(p, &m)
			switch m.Op {
			case 0, 7:
				ops <- m.Op
				if m.Op == 0 {
					_ = ws.WriteJSON(map[string]any{"op": 2, "s": 0, "d": map[string]any{"ssrc": 42}})
				} else {
					resumePayload <- m.D
					_ = ws.WriteJSON(map[string]any{"op": 9, "s": 2, "d": nil})
				}
			case 3:
				_ = ws.WriteJSON(map[string]any{"op": 6, "d": m.D})
				if n == 1 {
					_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4015, "test"), time.Now().Add(time.Second))
					return
				}
			}
		}
	}))
	defer server.Close()
	closed := make(chan error, 4)
	acks := make(chan struct{}, 8)
	type observationKey struct{}
	observations := make(chan string, 2)
	dave := godave.NewNoopSession(slog.Default(), "2", nil)
	g := newVoiceSocket(dave, func(observation context.Context, _ voice.Gateway, op voice.Opcode, _ int, _ voice.GatewayMessageData) {
		if op == voice.OpcodeReady || op == voice.OpcodeResumed {
			value, _ := observation.Value(observationKey{}).(string)
			observations <- value
		}
		if op == voice.OpcodeHeartbeatACK {
			select {
			case acks <- struct{}{}:
			default:
			}
		}
	}, func(_ voice.Gateway, err error, _ bool) { closed <- err }, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
	defer g.Close()
	channel := snowflake.ID(3)
	state := voice.State{GuildID: 1, UserID: 2, ChannelID: &channel, SessionID: "test", Token: "test", Endpoint: strings.TrimPrefix(server.URL, "https://")}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.Open(context.WithValue(ctx, observationKey{}, "initial"), state); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		var ce *websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != 4015 {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := g.Resume(context.WithValue(ctx, observationKey{}, "resume"), state); err != nil {
		t.Fatal(err)
	}
	if first, second := <-ops, <-ops; first != 0 || second != 7 {
		t.Fatalf("identify/resume: %d, %d", first, second)
	}
	var resumed map[string]json.RawMessage
	if err := json.Unmarshal(<-resumePayload, &resumed); err != nil {
		t.Fatal(err)
	}
	if string(resumed["seq_ack"]) != "0" || resumed["seq"] != nil || string(resumed["server_id"]) != `"1"` || string(resumed["session_id"]) != `"test"` || string(resumed["token"]) != `"test"` {
		t.Fatalf("invalid v8 Resume payload: %s", resumed)
	}
	for _, want := range []string{"initial", "resume"} {
		select {
		case got := <-observations:
			if got != want {
				t.Fatalf("event observation = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("establishment observation missing")
		}
	}
	if g.SSRC() != 42 {
		t.Fatal("Resume lost SSRC")
	}
	if err := g.Send(ctx, voice.OpcodeDaveMLSKeyPackage, voice.GatewayMessageDataDaveMLSKeyPackage{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-binary:
		if string(p) != string([]byte{byte(voice.OpcodeDaveMLSKeyPackage), 1, 2, 3}) {
			t.Fatalf("binary: %v", p)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Observe multiple real heartbeat/ACK cycles before racing teardown with it.
	for range 3 {
		select {
		case <-acks:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	g.Close()
	if connections.Load() != 2 || g.Status() != voice.StatusDisconnected {
		t.Fatal("unexpected connection lifecycle")
	}
	select {
	case err := <-closed:
		t.Fatalf("local close notified controller: %v", err)
	default:
	}
}

func TestVoiceSocketResumePreservesCloseCause(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4006, "Session no longer valid"))
	}))
	defer server.Close()
	for range 100 {
		g := newVoiceSocket(nil, nil, nil, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
		g.established = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := g.Resume(ctx, voice.State{Endpoint: strings.TrimPrefix(server.URL, "https://")})
		cancel()
		g.Close()
		var closed *websocket.CloseError
		if !errors.As(err, &closed) || closed.Code != 4006 {
			t.Fatalf("lost remote close cause: %v", err)
		}
	}
}

func TestVoiceSequencePresenceAndWrap(t *testing.T) {
	for _, tc := range []struct {
		kind    int
		payload []byte
		want    int
		present bool
	}{
		{websocket.TextMessage, []byte(`{"op":8,"d":{}}`), 0, false},
		{websocket.TextMessage, []byte(`{"s":0}`), 0, true},
		{websocket.TextMessage, []byte(`{"seq":0}`), 0, true},
		{websocket.TextMessage, []byte(`{"s":65535}`), 65535, true},
		{websocket.BinaryMessage, []byte{0, 0, 25}, 0, true},
		{websocket.BinaryMessage, []byte{255, 255, 25}, 65535, true},
	} {
		got, present := voiceSequence(tc.kind, tc.payload)
		if got != tc.want || present != tc.present {
			t.Fatalf("sequence: %d/%v want %d/%v", got, present, tc.want, tc.present)
		}
	}
}

func TestVoiceSocketRejectsContradictoryEstablishment(t *testing.T) {
	for _, tc := range []struct {
		name          string
		resume, hello bool
		op            int
	}{
		{"resumed during identify", false, true, 9},
		{"ready during resume", true, true, 2},
		{"ready before hello", false, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				if tc.hello {
					_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 60000}})
					if _, _, err := ws.ReadMessage(); err != nil {
						return
					}
				}
				_ = ws.WriteJSON(map[string]any{"op": tc.op, "d": map[string]int{"ssrc": 42}})
				_, _, _ = ws.ReadMessage()
			}))
			defer server.Close()
			g := newVoiceSocket(godave.NewNoopSession(slog.Default(), "2", nil), func(context.Context, voice.Gateway, voice.Opcode, int, voice.GatewayMessageData) {}, nil, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
			defer g.Close()
			g.established = tc.resume
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := g.open(ctx, voice.State{Endpoint: strings.TrimPrefix(server.URL, "https://")}, tc.resume)
			if err == nil || !strings.Contains(err.Error(), "unexpected voice establishment") {
				t.Fatalf("got %v", err)
			}
			if g.Status() != voice.StatusDisconnected {
				t.Fatal("invalid session became ready")
			}
		})
	}
}

func TestVoiceSocketHeartbeatTimeoutDoesNotReconnect(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		connections.Add(1)
		_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 20}})
		var m any
		if ws.ReadJSON(&m) != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]int{"ssrc": 42}})
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	closed := make(chan error, 2)
	g := newVoiceSocket(godave.NewNoopSession(slog.Default(), "2", nil), func(context.Context, voice.Gateway, voice.Opcode, int, voice.GatewayMessageData) {}, func(_ voice.Gateway, e error, _ bool) { closed <- e }, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
	defer g.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	channel := snowflake.ID(3)
	if err := g.Open(ctx, voice.State{ChannelID: &channel, Endpoint: strings.TrimPrefix(server.URL, "https://")}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("missing timeout")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if connections.Load() != 1 || g.Status() != voice.StatusDisconnected {
		t.Fatal("timeout reconnected autonomously")
	}
}
