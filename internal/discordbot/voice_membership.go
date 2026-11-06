package discordbot

import (
	"context"
	"time"
)

// A requested channel is not evidence of membership. Failed explicit starts
// must resolve membership before deciding whether a confirmed leave is needed.
func (c *voiceConnection) confirmRecoveryMembership(f *voiceRecoveryFlow) (bool, error) {
	if !c.recoveryStage(f, "membership_confirmation", 1) {
		return false, context.Canceled
	}
	ctx, cancel := context.WithTimeout(f.ctx, voiceStageTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		if !c.recoveryValidLocked(f) {
			c.mu.Unlock()
			return false, context.Canceled
		}
		known, channel, lookup, changed := c.membershipKnown, c.confirmedChannel, c.lookupMembership, c.changed
		c.mu.Unlock()
		if known {
			return channel != 0, nil
		}
		if lookup != nil {
			if observed, found := lookup(); found {
				c.mu.Lock()
				if c.recoveryValidLocked(f) && !c.membershipKnown {
					c.membershipKnown, c.confirmedChannel = true, observed
				}
				c.mu.Unlock()
				continue
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-changed:
		case <-ticker.C:
		}
	}
}
