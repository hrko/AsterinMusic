package discordbot

import (
	"testing"
	"time"
)

func TestIdleDisconnectPolicy(t *testing.T) {
	now := time.Now()
	s := &session{app: &App{}, idleSince: now.Add(-time.Hour)}
	if s.shouldDisconnect(now) {
		t.Fatal("idle must remain connected by default")
	}
	s.app.config.DisconnectWhenIdle = true
	if !s.shouldDisconnect(now) {
		t.Fatal("enabled idle timeout must disconnect")
	}
	s.idleSince = now.Add(-59 * time.Second)
	if s.shouldDisconnect(now) {
		t.Fatal("idle timeout fired too early")
	}
	s.app.config.DisconnectWhenIdle = false
	s.emptySince = now.Add(-time.Minute)
	if !s.shouldDisconnect(now) {
		t.Fatal("empty channel must still disconnect")
	}
	s.emptySince = now.Add(-59 * time.Second)
	if s.shouldDisconnect(now) {
		t.Fatal("empty timeout fired too early")
	}
}
