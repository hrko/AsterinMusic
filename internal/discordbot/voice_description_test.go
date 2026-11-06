package discordbot

import (
	"context"
	"crypto/tls"
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

type countedDescriptionDave struct {
	godave.Session
	acks      atomic.Int32
	external  atomic.Int32
	early     atomic.Bool
	processed chan struct{}
}

func (d *countedDescriptionDave) OnSelectProtocolAck(uint16) {
	d.early.Store(d.external.Load() == 1)
	d.acks.Add(1)
}
func (d *countedDescriptionDave) OnDaveMLSExternalSenderPackage([]byte) { d.external.Add(1) }
func (d *countedDescriptionDave) OnDavePrepareEpoch(int, uint16)        { close(d.processed) }

type countedKeyUDP struct {
	protocolTestUDP
	keys atomic.Int32
}

func (u *countedKeyUDP) SetSecretKey(voice.EncryptionMode, []byte) error { u.keys.Add(1); return nil }

func TestVoiceSessionDescriptionDuplicateDoesNotResetKeys(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "conflicting"}[conflict], func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 60000}})
				for {
					var m struct {
						Op int `json:"op"`
					}
					if ws.ReadJSON(&m) != nil {
						return
					}
					switch m.Op {
					case 0:
						_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
					case 1:
						// External sender information may precede the initial protocol
						// acknowledgement. Verify routing order, not MLS cryptography.
						_ = ws.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 25, 42})
						key := make([]int, 32)
						_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": key, "dave_protocol_version": 0}})
						if conflict {
							key[0] = 1
						}
						_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": key, "dave_protocol_version": 0}})
						_ = ws.WriteJSON(map[string]any{"op": 24, "d": map[string]int{"epoch": 1, "protocol_version": 0}})
					}
				}
			}))
			defer srv.Close()
			a := &App{}
			u := &countedKeyUDP{}
			d := &countedDescriptionDave{processed: make(chan struct{})}
			opts := []voice.ConnConfigOpt{
				voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn { return u }),
				voice.WithConnDaveSessionCreateFunc(func(l *slog.Logger, id godave.UserID, cb godave.Callbacks) godave.Session {
					d.Session = godave.NewNoopSession(l, id, cb)
					a.daveClosers.Store(cb, d)
					return d
				}),
			}
			closed := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			channel := snowflake.ID(3)
			transport, err := a.openVoiceTransportWithDialer(ctx, voice.State{GuildID: 1, UserID: 2, ChannelID: &channel, SessionID: "fixture", Token: "fixture", Endpoint: strings.TrimPrefix(srv.URL, "https://")}, func(err error, _ bool) { closed <- err }, opts, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})
			if transport != nil {
				defer transport.Close()
			}
			if conflict {
				if err == nil {
					select {
					case err = <-closed:
					case <-ctx.Done():
						t.Fatal("conflicting description accepted")
					}
				}
				var stage *voiceStageError
				if !errors.As(err, &stage) || stage.stage != "session_description" {
					t.Fatalf("conflict stage: %v", err)
				}
				if transport != nil && transport.Ready() {
					t.Fatal("conflicting transport stayed ready")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-d.processed:
				case <-ctx.Done():
					t.Fatal("event after duplicate was not processed")
				}
				if err := transport.Write(ctx, []byte{1}); err != nil {
					t.Fatal(err)
				}
			}
			if u.keys.Load() != 1 || d.acks.Load() != 1 {
				t.Fatalf("reinitialized: keys=%d DAVE=%d", u.keys.Load(), d.acks.Load())
			}
			if !d.early.Load() {
				t.Fatal("external sender notification did not reach DAVE before protocol acknowledgement")
			}
		})
	}
}
