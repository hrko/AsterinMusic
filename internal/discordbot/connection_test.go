package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

type delayedCloseConn struct {
	voice.Conn
	entered, release chan struct{}
}

func (c *delayedCloseConn) Close(context.Context) {
	close(c.entered)
	<-c.release
}

func TestDisconnectDoesNotHoldSessionLock(t *testing.T) {
	c := &delayedCloseConn{entered: make(chan struct{}), release: make(chan struct{})}
	s := newSession(&App{ctx: context.Background()}, 1)
	s.conn, s.channel = c, 2
	stopped := make(chan struct{})
	go func() { s.stop(); close(stopped) }()
	<-c.entered
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	defer func() {
		close(c.release)
		select {
		case <-closing:
		case <-time.After(time.Second):
			t.Error("disconnect cleanup did not finish")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing != nil {
			t.Error("disconnect guard not released")
		}
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop waited for connection close")
	}
	status := make(chan struct{})
	go func() {
		s.playbackStatus()
		s.mu.Lock()
		defer s.mu.Unlock()
		if !errors.Is(s.startLocked(2), errStopping) {
			t.Error("new connection allowed before old close")
		}
		if s.conn != nil || s.channel != 0 || s.closing == nil {
			t.Error("disconnect state not published")
		}
		close(status)
	}()
	select {
	case <-status:
	case <-time.After(time.Second):
		t.Fatal("status blocked on connection close")
	}
}
