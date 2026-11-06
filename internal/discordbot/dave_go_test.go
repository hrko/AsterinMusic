//go:build davego

package discordbot

import (
	"bytes"

	"errors"
	davesession "github.com/thomas-vilte/dave-go/session"
	"testing"
)

type daveCallbacks struct{}

func (daveCallbacks) SendMLSKeyPackage([]byte) error        { return nil }
func (daveCallbacks) SendMLSCommitWelcome([]byte) error     { return nil }
func (daveCallbacks) SendReadyForTransition(uint16) error   { return nil }
func (daveCallbacks) SendInvalidCommitWelcome(uint16) error { return nil }

func TestDaveHandshakeNeverSendsPlaintext(t *testing.T) {
	s := &guardedDave{Session: davesession.New("1234", daveCallbacks{})}
	defer s.Close()
	s.SetChannelID(42)
	s.AssignSsrcToCodec(7, 1)
	s.OnSelectProtocolAck(1)
	if s.Ready() {
		t.Fatal("handshake admitted frames")
	}
	p := []byte{0xf8, 0xff, 0xfe}
	out := make([]byte, 100)
	if n, err := s.Encrypt(7, p, out); n != 0 || !errors.Is(err, errDaveNotReady) {
		t.Fatalf("handshake must hold: n=%d err=%v", n, err)
	}
	// Explicitly negotiated protocol 0 permits transport encryption only.
	s.OnDavePrepareTransition(1, 0)
	s.OnDaveExecuteTransition(1)
	if !s.Ready() {
		t.Fatal("negotiated protocol zero must allow transport encryption")
	}
	n, err := s.Encrypt(7, p, out)
	if err != nil || !bytes.Equal(p, out[:n]) {
		t.Fatalf("negotiated v0: %v", err)
	}
}

func TestClosedDaveRejectsLateEventsAndAudio(t *testing.T) {
	s := &guardedDave{Session: davesession.New("1234", daveCallbacks{})}
	s.OnSelectProtocolAck(0)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.SetChannelID(42)
	s.OnSelectProtocolAck(1)
	s.OnDavePrepareTransition(1, 0)
	s.OnDaveExecuteTransition(1)
	if s.Ready() {
		t.Fatal("closed DAVE became ready")
	}
	if _, err := s.Encrypt(7, []byte{1}, make([]byte, 100)); !errors.Is(err, errDaveNotReady) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("close not idempotent", err)
	}
}
