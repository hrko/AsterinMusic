package discordbot

import (
	"github.com/disgoorg/disgo/voice"
	"io"
)

// disgo v0.19.6 does not call godave.Session.Close when discarding a voice
// connection. Explicitly release pure-Go recovery watchdogs on every exit path.
func (a *App) closeDave(conn voice.Conn) {
	if closer, ok := a.daveClosers.LoadAndDelete(conn); ok {
		_ = closer.(io.Closer).Close()
	}
}
func (a *App) closeAllDave() {
	a.daveClosers.Range(func(key, value any) bool {
		if closer, ok := a.daveClosers.LoadAndDelete(key); ok {
			_ = closer.(io.Closer).Close()
		}
		return true
	})
}
