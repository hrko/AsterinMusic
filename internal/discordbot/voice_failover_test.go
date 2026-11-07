package discordbot

import (
	"testing"
)

func TestVoiceServerOnlyFailover(t *testing.T) {
	for _, mode := range []string{"token", "endpoint", "null"} {
		t.Run(mode, func(t *testing.T) {
			c := testVoiceController(t)
			c.HandleVoiceStateUpdate(stateEvent(3))
			c.HandleVoiceServerUpdate(serverEvent("initial"))
			waitVoice(t, c)
			c.mu.Lock()
			old, generation := c.active.(*fakeVoiceTransport), c.generation
			c.mu.Unlock()
			e := serverEvent("initial")
			switch mode {
			case "token":
				e.Token = "replacement"
			case "endpoint":
				endpoint := "replacement"
				e.Endpoint = &endpoint
			case "null":
				null := e
				null.Endpoint = nil
				c.HandleVoiceServerUpdate(null)
			}
			c.HandleVoiceServerUpdate(e)
			waitVoice(t, c)
			c.mu.Lock()
			if !old.invalid.Load() || c.generation == generation || c.channel != 3 {
				t.Fatal("server-only refresh did not replace transport")
			}
			c.mu.Unlock()
			// A delayed membership notification may reveal a move after the
			// server-only attempt has already completed. Its credentials survive.
			c.HandleVoiceStateUpdate(stateEvent(4))
			waitVoice(t, c)
			if id := c.ChannelID(); id == nil || *id != 4 {
				t.Fatal("late membership did not move connection")
			}
		})
	}
}
