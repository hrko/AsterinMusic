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

func TestVoiceSocketHeartbeatResumeAndBinary(t *testing.T) {
	var connections atomic.Int32
	ops := make(chan int, 4)
	binary := make(chan []byte, 1)
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
					_ = ws.WriteJSON(map[string]any{"op": 2, "s": 1, "d": map[string]any{"ssrc": 42}})
				} else {
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
	dave := godave.NewNoopSession(slog.Default(), "2", nil)
	g := newVoiceSocket(dave, func(_ voice.Gateway, op voice.Opcode, _ int, _ voice.GatewayMessageData) {
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
	if err := g.Open(ctx, state); err != nil {
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
	if err := g.Open(ctx, state); err != nil {
		t.Fatal(err)
	}
	if first, second := <-ops, <-ops; first != 0 || second != 7 {
		t.Fatalf("identify/resume: %d, %d", first, second)
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
	g := newVoiceSocket(godave.NewNoopSession(slog.Default(), "2", nil), func(voice.Gateway, voice.Opcode, int, voice.GatewayMessageData) {}, func(_ voice.Gateway, e error, _ bool) { closed <- e }, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
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
