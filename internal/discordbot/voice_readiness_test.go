package discordbot

import (
	"testing"
	"time"
)

func TestDaveRecoveryWithoutAudioWrites(t *testing.T) {
	c := testVoiceController(t)
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("fixture"))
	waitVoice(t, c)
	c.mu.Lock()
	transport, generation := c.active.(*fakeVoiceTransport), c.generation
	c.mu.Unlock()
	transport.ready.Store(false)
	until := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		phase := c.phase
		c.mu.Unlock()
		if phase == voiceRecovering {
			break
		}
		if time.Now().After(until) {
			t.Fatal("no-write key loss was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	transport.ready.Store(true)
	for {
		c.mu.Lock()
		restored := c.phase == voiceReady && c.deadline.IsZero()
		c.mu.Unlock()
		if restored {
			break
		}
		if time.Now().After(until) {
			t.Fatal("no-write key recovery left deadline armed")
		}
		time.Sleep(time.Millisecond)
	}
	if transport.writes.Load() != 0 {
		t.Fatal("test unexpectedly sent audio")
	}
	c.observeReadiness(generation-1, transport, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != voiceReady {
		t.Fatal("stale readiness damaged current generation")
	}
}
