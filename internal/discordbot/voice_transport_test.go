package discordbot

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	closed  atomic.Bool
}

func (u *protocolTestUDP) Open(context.Context, string, int, uint32) (string, int, error) {
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
			case 0:
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
		if !u.closed.Load() || u.written.Load() != 1 {
			t.Fatal("teardown or send failed")
		}
	}
	a.daveClosers.Range(func(any, any) bool { t.Error("DAVE resource leaked"); return true })
}
