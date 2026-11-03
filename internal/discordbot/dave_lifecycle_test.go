package discordbot

import (
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"log/slog"
	"testing"
)

func TestDisgoConnectionFactoryUsesConnectionAsDaveCallbacks(t *testing.T) {
	a := &App{}
	var callbacks []godave.Callbacks
	var closers []*countedCloser
	factory := func(logger *slog.Logger, user godave.UserID, callback godave.Callbacks) godave.Session {
		closer := &countedCloser{}
		callbacks = append(callbacks, callback)
		closers = append(closers, closer)
		a.daveClosers.Store(callback, closer)
		return godave.NewNoopSession(logger, user, callback)
	}
	// Same guild, different connection generations: guild-only keys would
	// allow teardown of the first connection to destroy the second session.
	first := voice.NewConn(1, 2, nil, func() {}, voice.WithConnDaveSessionCreateFunc(factory))
	second := voice.NewConn(1, 2, nil, func() {}, voice.WithConnDaveSessionCreateFunc(factory))
	if any(callbacks[0]) != any(first) || any(callbacks[1]) != any(second) {
		t.Fatal("disgo changed callback/connection identity; review DAVE cleanup keys")
	}
	a.closeDave(first)
	if closers[0].count != 1 || closers[1].count != 0 {
		t.Fatal("old connection closed new DAVE session")
	}
	if _, ok := a.daveClosers.Load(second); !ok {
		t.Fatal("new connection cleanup registration lost")
	}
	a.closeDave(second)
	a.closeAllDave()
	if closers[0].count != 1 || closers[1].count != 1 {
		t.Fatal("connection cleanup was not exactly once")
	}
}

type countedCloser struct{ count int }

func (c *countedCloser) Close() error { c.count++; return nil }
func TestDaveConnectionCleanupOnce(t *testing.T) {
	a := &App{}
	conn := &struct{ voice.Conn }{}
	first, second := &countedCloser{}, &countedCloser{}
	a.daveClosers.Store(conn, first)
	a.daveClosers.Store("orphan after failed open", second)
	a.closeDave(conn)
	a.closeDave(conn)
	if first.count != 1 {
		t.Fatalf("connection closed %d times", first.count)
	}
	a.closeAllDave()
	a.closeAllDave()
	if first.count != 1 || second.count != 1 {
		t.Fatal("shutdown leaked or double-closed a DAVE session")
	}
}
