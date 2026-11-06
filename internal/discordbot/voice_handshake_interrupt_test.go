package discordbot

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/gorilla/websocket"
)

type interruptedDiscoveryUDP struct{ protocolTestUDP }

func (*interruptedDiscoveryUDP) Open(ctx context.Context, _ string, _ int, _ uint32) (string, int, error) {
	<-ctx.Done()
	return "", 0, ctx.Err()
}

// Key installation has no context parameter. Model a stuck implementation
// whose real Close interrupts it, rather than treating cancellation as exit.
type interruptedKeyUDP struct {
	protocolTestUDP
	done chan struct{}
	once sync.Once
}

func (u *interruptedKeyUDP) SetSecretKey(voice.EncryptionMode, []byte) error {
	<-u.done
	return net.ErrClosed
}

func (u *interruptedKeyUDP) Close() error {
	u.once.Do(func() { close(u.done) })
	return nil
}

// The TLS connection is real; only the selected application write is held.
// Closing it must release the blocked Write, as closing an OS socket would.
type interruptedVoiceWrite struct {
	net.Conn
	op              int
	entered, done   chan struct{}
	once, enterOnce sync.Once
}

func (c *interruptedVoiceWrite) Write(p []byte) (int, error) {
	// Decode the small, single-frame client message emitted by gorilla. Skip
	// the HTTP upgrade and control frames; heartbeat timing is irrelevant.
	if len(p) >= 6 && p[0] == 0x81 && p[1]&0x80 != 0 {
		n, offset := int(p[1]&0x7f), 2
		if n == 126 && len(p) >= 8 {
			n, offset = int(binary.BigEndian.Uint16(p[2:4])), 4
		}
		if n < 65536 && n != 127 && len(p) == offset+4+n {
			body := make([]byte, n)
			for i := range body {
				body[i] = p[offset+4+i] ^ p[offset+i%4]
			}
			var message struct {
				Op int `json:"op"`
			}
			if json.Unmarshal(body, &message) == nil && message.Op == c.op {
				c.enterOnce.Do(func() { close(c.entered) })
				<-c.done
				return 0, net.ErrClosed
			}
		}
	}
	return c.Conn.Write(p)
}

func (c *interruptedVoiceWrite) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

// The first factory runs the real wire/handshake implementation against a
// stalled local server. A move can establish a new generation independently.
func TestVoiceHandshakeInterruptedAtWireStages(t *testing.T) {
	for _, stage := range []string{"websocket_connect", "hello", "identify", "ready", "udp_discovery", "select_protocol", "session_description", "transport_key", "dave"} {
		for _, action := range []string{"stop", "move", "external_leave"} {
			t.Run(stage+"/"+action, func(t *testing.T) {
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stage == "websocket_connect" {
						<-r.Context().Done()
						return
					}
					ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer ws.Close()
					if stage != "hello" {
						_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]int{"heartbeat_interval": 60000}})
					}
					for {
						var m struct {
							Op int `json:"op"`
						}
						if ws.ReadJSON(&m) != nil {
							return
						}
						if m.Op == 0 && stage != "ready" {
							_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
						}
						if m.Op == 1 && (stage == "dave" || stage == "transport_key") {
							_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": make([]int, 32), "dave_protocol_version": 0}})
						}
					}
				}))
				defer server.Close()
				c := testVoiceController(t)
				// Teardown must cancel the client before httptest waits for handlers.
				defer c.Close(context.Background())
				a := &App{}
				writeEntered := make(chan struct{})
				c.factory = func(ctx context.Context, state voice.State, closed func(error, bool)) (voiceTransport, error) {
					if *state.ChannelID == 4 {
						return newFakeVoiceTransport(), nil
					}
					state.Endpoint = strings.TrimPrefix(server.URL, "https://")
					opts := []voice.ConnConfigOpt{
						voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn {
							if stage == "udp_discovery" {
								return &interruptedDiscoveryUDP{}
							}
							if stage == "transport_key" {
								return &interruptedKeyUDP{done: make(chan struct{})}
							}
							return &protocolTestUDP{}
						}),
						voice.WithConnDaveSessionCreateFunc(func(log *slog.Logger, user godave.UserID, cb godave.Callbacks) godave.Session {
							d := &pendingDaveSession{Session: godave.NewNoopSession(log, user, cb)}
							a.daveClosers.Store(cb, d)
							return d
						}),
					}
					dialer := &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
					if stage == "identify" || stage == "select_protocol" {
						op := 0
						if stage == "select_protocol" {
							op = 1
						}
						dialer.NetDialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
							conn, err := (&tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}).DialContext(ctx, network, address)
							if err != nil {
								return nil, err
							}
							return &interruptedVoiceWrite{Conn: conn, op: op, entered: writeEntered, done: make(chan struct{})}, nil
						}
					}
					return a.openVoiceTransportWithDialer(ctx, state, closed, opts, dialer)
				}
				c.HandleVoiceStateUpdate(stateEvent(3))
				c.HandleVoiceServerUpdate(serverEvent("fixture"))
				deadline := time.Now().Add(2 * time.Second)
				for {
					c.mu.Lock()
					attempt := c.traceAttempt
					c.mu.Unlock()
					if attempt != nil && attempt.current() == stage {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("handshake did not reach " + stage)
					}
					time.Sleep(time.Millisecond)
				}
				if stage == "identify" || stage == "select_protocol" {
					select {
					case <-writeEntered:
					case <-time.After(time.Second):
						t.Fatal("selected wire write was not reached")
					}
				}
				c.mu.Lock()
				lease, generation := c.resourceLease, c.generation
				c.mu.Unlock()
				switch action {
				case "stop":
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					c.Close(ctx)
				case "move":
					c.HandleVoiceStateUpdate(stateEvent(4))
					c.HandleVoiceServerUpdate(serverEvent("new"))
					waitVoice(t, c)
				case "external_leave":
					e := stateEvent(0)
					e.ChannelID = nil
					c.HandleVoiceStateUpdate(e)
				}
				select {
				case <-lease.done:
				case <-time.After(time.Second):
					t.Fatal("interrupted factory/socket/worker did not finish")
				}
				c.transportClosed(generation, &websocket.CloseError{Code: 4006}, true)
				c.mu.Lock()
				phase, channel := c.phase, c.channel
				c.mu.Unlock()
				if action == "move" {
					if phase != voiceReady || channel != 4 {
						t.Fatal("old handshake changed moved connection")
					}
					if err := c.WriteAudio(context.Background(), []byte{1}); err != nil {
						t.Fatal(err)
					}
				} else if phase != voiceClosed && phase != voiceFailed {
					t.Fatal("interrupted handshake resumed playback")
				}
				a.daveClosers.Range(func(any, any) bool { t.Error("DAVE registration leaked"); return true })
			})
		}
	}
}
