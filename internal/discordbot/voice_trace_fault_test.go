package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type pendingDaveSession struct {
	godave.Session
	checked chan struct{}
	once    sync.Once
}

func (s *pendingDaveSession) Ready() bool {
	if s.checked != nil {
		s.once.Do(func() { close(s.checked) })
	}
	return false
}

type failedDiscoveryUDP struct{ protocolTestUDP }

func (*failedDiscoveryUDP) Open(context.Context, string, int, uint32) (string, int, error) {
	return "", 0, errors.New("synthetic-secret: discovery failed")
}

// These cases run the real gateway/handshake code. The fixture withholds the
// selected response instead of directly calling the tracing helper.
func TestVoiceTransportTraceFaultStages(t *testing.T) {
	for _, fault := range []string{"gateway", "udp_protocol_transport_key", "udp_discovery", "dave", "dave_closed"} {
		t.Run(fault, func(t *testing.T) {
			stage := fault
			checked := make(chan struct{})
			if fault == "dave_closed" {
				stage = "dave"
			}
			if fault == "gateway" {
				stage = "websocket_connect"
			}
			if fault == "udp_protocol_transport_key" {
				stage = "session_description"
			}
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			defer provider.Shutdown(context.Background())
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fault == "gateway" {
					http.Error(w, "synthetic-secret", http.StatusForbidden)
					return
				}
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]any{"heartbeat_interval": 60000}})
				for {
					var message struct {
						Op int `json:"op"`
					}
					if err := ws.ReadJSON(&message); err != nil {
						return
					}
					switch message.Op {
					case 0:
						_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 42, "ip": "127.0.0.1", "port": 9000, "modes": []string{"aead_aes256_gcm_rtpsize"}}})
					case 1:
						if stage == "dave" {
							_ = ws.WriteJSON(map[string]any{"op": 4, "d": map[string]any{"mode": "aead_aes256_gcm_rtpsize", "secret_key": make([]int, 32), "dave_protocol_version": 0}})
							if fault == "dave_closed" {
								select {
								case <-checked:
								case <-time.After(time.Second):
									return
								}
								_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4006, "synthetic-secret"), time.Now().Add(time.Second))
								return
							}
						}
					}
				}
			}))
			defer srv.Close()
			var logs logBuffer
			logger, logErr := logging.New(&logs, logging.Options{Level: "info", Format: "json", Components: map[string]string{"voice": "debug"}})
			if logErr != nil {
				t.Fatal(logErr)
			}
			a := &App{tracer: provider.Tracer("test"), logger: logger}
			opts := []voice.ConnConfigOpt{
				voice.WithUDPConnCreateFunc(func(godave.Session, voice.SsrcLookupFunc, ...voice.UDPConnConfigOpt) voice.UDPConn {
					if fault == "udp_discovery" {
						return &failedDiscoveryUDP{}
					}
					return &protocolTestUDP{}
				}),
				voice.WithConnDaveSessionCreateFunc(func(logger *slog.Logger, user godave.UserID, cb godave.Callbacks) godave.Session {
					var session godave.Session = godave.NewNoopSession(logger, user, cb)
					if stage == "dave" {
						session = &pendingDaveSession{Session: session, checked: checked}
					}
					a.daveClosers.Store(cb, session)
					return session
				}),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			ctx, span := a.tracer.Start(ctx, "voice.connect")
			attempt := &voiceTraceAttempt{ctx: ctx, span: span, tracer: a.tracer}
			ctx = context.WithValue(ctx, voiceTraceKey{}, attempt)
			controller := newVoiceConnection(1, 2)
			controller.logger = logging.Component(logger, "voice")
			ctx = controller.diagnosticContext(ctx)
			channel := snowflake.ID(3)
			_, err := a.openVoiceTransportWithDialer(ctx, voice.State{GuildID: 1, UserID: 2, ChannelID: &channel, SessionID: "synthetic-secret", Token: "synthetic-secret", Endpoint: strings.TrimPrefix(srv.URL, "https://")}, func(error, bool) {}, opts, &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}) // local fixture only
			attempt.end(err)
			controller.flushDiagnostics()
			if !strings.Contains(logs.String(), `"event":"voice.handshake.failed"`) || strings.Contains(logs.String(), "synthetic-secret") {
				t.Fatal("missing safe voice-component failure diagnostic")
			}
			if err == nil {
				t.Fatal("fault did not fail")
			}
			if fault == "dave_closed" {
				var remote *websocket.CloseError
				if !errors.As(err, &remote) || remote.Code != 4006 {
					t.Fatalf("DAVE wait lost terminal cause: %v", err)
				}
			}
			if fault == "udp_discovery" {
				var stageErr *voiceStageError
				if !errors.As(err, &stageErr) || stageErr.stage != "udp_discovery" || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("discovery failure was replaced by timeout/key error: %v", err)
				}
			}
			if attempt.current() != stage {
				t.Fatalf("wrong stage: %s", attempt.current())
			}
			if len(recorder.Started()) != len(recorder.Ended()) {
				t.Fatal("unfinished span")
			}
			for _, observed := range recorder.Ended() {
				if observed.Name() == "voice.connect" || observed.Name() == "voice."+stage {
					if observed.Status().Code != codes.Error {
						t.Errorf("%s lost failure", observed.Name())
					}
				} else if observed.Status().Code != codes.Unset {
					t.Errorf("earlier stage %s incorrectly failed", observed.Name())
				}
				data, _ := json.Marshal(struct {
					Attributes any
					Events     any
					Status     any
				}{observed.Attributes(), observed.Events(), observed.Status()})
				if strings.Contains(string(data), "synthetic-secret") {
					t.Fatal("external secret leaked into span")
				}
			}
			a.daveClosers.Range(func(any, any) bool { t.Error("DAVE resource leaked"); return true })
		})
	}
}
