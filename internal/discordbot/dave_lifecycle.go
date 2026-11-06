package discordbot

import (
	"github.com/disgoorg/disgo/voice"
	"io"
)

// disgo v0.19.6 does not call godave.Session.Close when discarding a voice
// connection. Explicitly release pure-Go recovery watchdogs on every exit path.
func (a *App) closeDave(conn voice.Conn) {
	a.takeDaveCleanup(conn)()
}

func (a *App) takeDaveCleanup(conn voice.Conn) func() {
	if closer, ok := a.daveClosers.LoadAndDelete(conn); ok {
		return func() { _ = closer.(io.Closer).Close() }
	}
	return func() {}
}
func (a *App) closeAllDave() {
	a.daveClosers.Range(func(key, value any) bool {
		if closer, ok := a.daveClosers.LoadAndDelete(key); ok {
			_ = closer.(io.Closer).Close()
		}
		return true
	})
}
