package discordbot

import (
	"context"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/voice"
)

func TestDaveCallbackFailureInvalidatesAndRejectsLateSends(t *testing.T) {
	for _, op := range []voice.Opcode{voice.OpcodeDaveMLSKeyPackage, voice.OpcodeDaveMLSCommitWelcome, voice.OpcodeDaveTransitionReady, voice.OpcodeDaveMLSInvalidCommitWelcome} {
		valid, failures := true, 0
		g := &voiceSocket{valid: func() bool { return valid }, daveFailed: func(error) { failures++; valid = false }}
		err := g.Send(context.Background(), op, nil)
		if !errors.Is(err, errDaveTransportUnsafe) || !errors.Is(err, voice.ErrGatewayNotConnected) {
			t.Fatalf("lost callback failure or cause: %v", err)
		}
		if resumeAllowed(err) || reconnectAllowed(err) {
			t.Fatal("unsafe MLS state allowed automatic reuse")
		}
		if failureClass(err) != "dave_send_failed" {
			t.Fatal("callback error not classified")
		}
		if err := g.Send(context.Background(), op, nil); !errors.Is(err, context.Canceled) || failures != 1 {
			t.Fatal("retired callback started another recovery")
		}
	}
}

func TestDaveCallbackFailureCannotReplaceNewGeneration(t *testing.T) {
	c := testVoiceController(t)
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("first"))
	waitVoice(t, c)
	c.mu.Lock()
	old := c.generation
	c.mu.Unlock()
	c.HandleVoiceStateUpdate(stateEvent(4))
	c.HandleVoiceServerUpdate(serverEvent("second"))
	waitVoice(t, c)
	c.transportClosed(old, &daveSendError{cause: voice.ErrGatewayNotConnected}, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != voiceReady || c.generation == old {
		t.Fatal("old DAVE failure changed replacement connection")
	}
}

func TestDaveCallbackFailureDoesNotResumeUnsafeSession(t *testing.T) {
	c := testVoiceController(t)
	transport := &resumableFakeTransport{newFakeVoiceTransport(), make(chan struct{}), make(chan struct{})}
	c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) { return transport, nil }
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("first"))
	waitVoice(t, c)
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	c.transportClosed(generation, &daveSendError{cause: voice.ErrGatewayNotConnected}, false)
	c.mu.Lock()
	failed := c.phase == voiceFailed && c.failureStage == "dave_send"
	c.mu.Unlock()
	if !failed {
		t.Fatal("unsafe DAVE connection did not enter held failure")
	}
	select {
	case <-transport.called:
		t.Fatal("unsafe DAVE session resumed")
	default:
	}
}
