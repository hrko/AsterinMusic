package discordbot

import (
	"context"
	"errors"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"testing"
)

type testMoveGateway struct {
	voice.Gateway
	event   voice.EventHandlerFunc
	onClose voice.CloseHandlerFunc
	closed  bool
}

func (g *testMoveGateway) Open(context.Context, voice.State) error { return nil }
func (g *testMoveGateway) Close()                                  { g.closed = true }
func (g *testMoveGateway) Status() voice.Status                    { return voice.StatusReady }

func TestVoiceGatewayMove4014AndRepeatedRekeys(t *testing.T) {
	target := snowflake.ID(3)
	var bases []*testMoveGateway
	delivered, rekeys, closed, udpClosed := 0, 0, 0, 0
	g := &movingGateway{factory: func(e voice.EventHandlerFunc, c voice.CloseHandlerFunc) voice.Gateway {
		b := &testMoveGateway{event: e, onClose: c}
		bases = append(bases, b)
		return b
	},
		event:   func(voice.Gateway, voice.Opcode, int, voice.GatewayMessageData) { delivered++ },
		onClose: func(voice.Gateway, error, bool) { closed++ }, currentChannel: func() snowflake.ID { return target },
		rekey: func(voice.GatewayMessageDataSessionDescription) error { rekeys++; return nil }, closeUDP: func() { udpClosed++ },
	}
	state := voice.State{ChannelID: &target, SessionID: "session", Endpoint: "endpoint", Token: "test"}
	if err := g.Open(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if g.Status() == voice.StatusReady {
		t.Fatal("ready before key installation")
	}
	bases[0].event(bases[0], voice.OpcodeSessionDescription, 1, voice.GatewayMessageDataSessionDescription{})
	if delivered != 1 || g.Status() != voice.StatusReady {
		t.Fatal("initial open")
	}
	target = 4 // Move state can arrive before a new voice-server update.
	bases[0].onClose(bases[0], &websocket.CloseError{Code: 4014}, false)
	if closed != 0 || g.Status() == voice.StatusReady {
		t.Fatal("move disconnected VC or retained ready flag")
	}
	for i := range 3 {
		target = snowflake.ID(i + 4)
		state.ChannelID = &target
		if err := g.Open(context.Background(), state); err != nil {
			t.Fatal(err)
		}
		current := bases[len(bases)-1]
		current.event(current, voice.OpcodeSessionDescription, 2, voice.GatewayMessageDataSessionDescription{})
		// Old close/events may arrive after the new gateway has become ready.
		bases[0].onClose(bases[0], errors.New("late close"), false)
		bases[0].event(bases[0], voice.OpcodeSessionDescription, 3, voice.GatewayMessageDataSessionDescription{})
		if closed != 0 || g.Status() != voice.StatusReady {
			t.Fatal("old generation damaged new connection")
		}
	}
	if delivered != 1 || rekeys != 3 || udpClosed != 3 || !bases[0].closed {
		t.Fatal(delivered, rekeys, udpClosed)
	}
	target = 0 // An actual removal must still reach the original close handler.
	current := bases[len(bases)-1]
	current.onClose(current, &websocket.CloseError{Code: 4014}, false)
	if closed != 1 {
		t.Fatal("external removal swallowed")
	}
	g.Close()
	if err := g.Open(context.Background(), state); !errors.Is(err, voice.ErrGatewayNotConnected) {
		t.Fatal("reopened closed connection", err)
	}
}

func TestVoiceGatewayFailedRekeyDoesNotSendAudio(t *testing.T) {
	var base *testMoveGateway
	g := &movingGateway{described: true, factory: func(e voice.EventHandlerFunc, c voice.CloseHandlerFunc) voice.Gateway {
		base = &testMoveGateway{event: e, onClose: c}
		return base
	}, rekey: func(voice.GatewayMessageDataSessionDescription) error { return errors.New("invalid key") }}
	id := snowflake.ID(3)
	if err := g.Open(context.Background(), voice.State{ChannelID: &id}); err != nil {
		t.Fatal(err)
	}
	base.event(base, voice.OpcodeSessionDescription, 1, voice.GatewayMessageDataSessionDescription{})
	if g.Status() == voice.StatusReady {
		t.Fatal("failed key installation admitted audio")
	}
}
