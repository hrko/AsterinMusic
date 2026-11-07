package discordbot

import (
	"context"
	"time"
)

// Transport.Ready is a cached, nonblocking observation in production. Tracking
// it belongs to the connection lifetime, not to a particular track's Write.
func (c *voiceConnection) monitorReadiness(ctx context.Context, generation uint64, t voiceTransport) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.observeReadiness(generation, t, t.Ready())
			c.flushDiagnostics()
		}
	}
}

func (c *voiceConnection) observeReadiness(generation uint64, t voiceTransport, ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.active != t || c.resuming || c.recovery != nil || (c.phase != voiceReady && c.phase != voiceRecovering) {
		return
	}
	if ready && c.phase == voiceRecovering {
		c.deadline = time.Time{}
		if c.timer != nil {
			c.timer.Stop()
		}
		c.signalLocked(voiceReady)
	} else if !ready && c.phase == voiceReady {
		c.recoveryStart = c.now()
		c.deadline = c.now().Add(voiceRecoveryTimeout)
		c.failureStage = "dave_ready"
		c.signalLocked(voiceRecovering)
		c.armLocked()
	}
}
