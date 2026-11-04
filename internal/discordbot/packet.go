package discordbot

import (
	"context"
	"time"
)

// The playback loop serializes sends. session.mu protects admission and state
// publication, but never the network write. One admitted packet may still be
// in flight when cancellation occurs; its completion cannot revive playback.
func (s *session) sendPacket(ctx context.Context, write func() error) error {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.stopped || s.shuttingDown {
		s.mu.Unlock()
		return context.Canceled
	}
	current := s.current
	if current != nil && !s.started && s.currentAuto && !s.replaying {
		_, pending := s.queue.Snapshot()
		if !s.auto.enabled || s.auto.revision != s.currentRevision || len(pending) > 0 {
			s.mu.Unlock()
			return errSuperseded
		}
	}
	s.mu.Unlock()
	if err := write(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.stopped || s.shuttingDown || s.current != current {
		return context.Canceled
	}
	if current != nil && !s.started {
		if c, ok := s.conn.(*voiceConnection); ok {
			if id := c.ChannelID(); id != nil {
				s.moveVoiceLocked(*id)
			}
		}
		s.started = true
		s.playbackSequence++
		queued := s.recordStartedLocked()
		if p := s.playbackLog; p != nil {
			p.started <- playbackStart{channel: s.channel.String(), at: time.Now(), historyFailed: !queued}
		}
		s.prefetchLocked()
	}
	s.position += 20 * time.Millisecond
	return nil
}
