package discordbot

import (
	"github.com/disgoorg/disgo/voice"
	"testing"
)

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
