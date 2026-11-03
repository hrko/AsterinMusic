package discordbot

import (
	"asterinmusic/internal/music"
	"context"
	"errors"
	"github.com/disgoorg/snowflake/v2"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

type delayedOpenConn struct {
	voice.Conn
	entered, release, closed chan struct{}
}

func (c *delayedOpenConn) Open(context.Context, snowflake.ID, bool, bool) error {
	close(c.entered)
	<-c.release // Simulate a late success even after cancellation.
	return nil
}
func (c *delayedOpenConn) Close(context.Context) { close(c.closed) }

func TestStopDuringOpenRejectsLateConnection(t *testing.T) {
	c := &delayedOpenConn{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	s := newSession(&App{ctx: context.Background()}, 1)
	s.queue.Add(music.Requester{ID: "a"}, []music.Track{{URL: "track"}}, "", "")
	s.createConn = func() voice.Conn { return c }
	result := make(chan error, 1)
	go func() { s.mu.Lock(); defer s.mu.Unlock(); result <- s.startLocked(2) }()
	<-c.entered
	status := make(chan bool, 1)
	go func() { status <- s.playbackStatus().Active }()
	select {
	case active := <-status:
		if !active {
			t.Error("connecting not reported active")
		}
	case <-time.After(time.Second):
		close(c.release)
		t.Fatal("status blocked during Open")
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	close(c.release)
	select {
	case err := <-result:
		if !errors.Is(err, errQueueChanged) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("late connection not discarded")
	}
	<-c.closed
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil || s.running || s.connecting || s.closing != nil {
		t.Fatal("late success revived playback")
	}
	_, pending := s.queue.Snapshot()
	if len(pending) != 1 {
		t.Fatal("queued work lost")
	}
}

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
