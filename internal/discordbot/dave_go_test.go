//go:build davego

package discordbot

import (
	"bytes"
	"context"
	"errors"
	davesession "github.com/thomas-vilte/dave-go/session"
	"testing"
	"time"
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
	p := []byte{0xf8, 0xff, 0xfe}
	out := make([]byte, 100)
	if n, err := s.Encrypt(7, p, out); n != 0 || !errors.Is(err, errDaveNotReady) {
		t.Fatalf("handshake must hold: n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitDave(ctx, func() error { _, err := s.Encrypt(7, p, out); return err }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// Explicitly negotiated protocol 0 permits transport encryption only.
	s.OnDavePrepareTransition(1, 0)
	s.OnDaveExecuteTransition(1)
	n, err := s.Encrypt(7, p, out)
	if err != nil || !bytes.Equal(p, out[:n]) {
		t.Fatalf("negotiated v0: %v", err)
	}
}

func TestDaveRetrySameFrame(t *testing.T) {
	attempts := 0
	if err := waitDave(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errDaveNotReady
		}
		return nil
	}); err != nil || attempts != 3 {
		t.Fatalf("%d %v", attempts, err)
	}
	terminal := errors.New("network failed")
	if err := waitDave(context.Background(), func() error { return terminal }); !errors.Is(err, terminal) {
		t.Fatal(err)
	}
}

func TestDaveMoveCreatesFreshSession(t *testing.T) {
	factory := func() *davesession.Session { return davesession.New("1234", daveCallbacks{}) }
	s := &guardedDave{Session: factory(), factory: factory}
	defer s.Close()
	s.SetChannelID(42)
	s.OnSelectProtocolAck(1)
	old := s.Session
	s.ResetVoiceTransport(43)
	if s.Session == old {
		t.Fatal("move reused prior MLS session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := old.WaitShutdown(ctx); err != nil {
		t.Fatal("old session was not closed", err)
	}
	s.OnSelectProtocolAck(1)
	s.AssignSsrcToCodec(7, 1)
	if _, err := s.Encrypt(7, []byte{0xf8, 0xff, 0xfe}, make([]byte, 100)); !errors.Is(err, errDaveNotReady) {
		t.Fatal("fresh session must wait for new keys", err)
	}
	s.Close()
	last := s.Session
	s.ResetVoiceTransport(44)
	if s.Session != last {
		t.Fatal("closed session resurrected")
	}
}
