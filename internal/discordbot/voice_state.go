package discordbot

import (
	"context"
	"errors"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"log/slog"
	"time"
)

var errVoiceMoveFailed = errors.New("voice move transport recovery failed")

// Retry the same encoded packet during a bounded move transition. The producer
// remains blocked, so this never restarts or skips ahead in the current track.
func (s *session) sendVoicePacket(ctx context.Context, send func() error) error {
	started := time.Now()
	for {
		s.mu.Lock()
		moveDeadline := s.moveDeadline
		s.mu.Unlock()
		attemptCtx, cancel := context.WithCancel(ctx)
		if !moveDeadline.IsZero() && started.Before(moveDeadline) {
			cancel()
			if !time.Now().Before(moveDeadline) {
				return errVoiceMoveFailed
			}
			attemptCtx, cancel = context.WithDeadline(ctx, moveDeadline)
		}
		err := waitDave(attemptCtx, send)
		cancel()
		if err == nil || ctx.Err() != nil || errors.Is(err, errSuperseded) {
			return err
		}
		s.mu.Lock()
		deadline := s.moveDeadline
		stopped := s.stopped || s.shuttingDown
		s.mu.Unlock()
		if stopped {
			return context.Canceled
		}
		if deadline.IsZero() || !started.Before(deadline) {
			return err
		}
		if !time.Now().Before(deadline) {
			return errVoiceMoveFailed
		}
		timer := time.NewTimer(min(100*time.Millisecond, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// The voice library follows server/channel changes on the existing connection.
// Keep the playback session (and its first-packet history) intact while updating
// authorization and notification boundaries. Called with session.mu held.
func (s *session) syncVoiceLocked() bool {
	state, ok := s.app.client.Caches.VoiceState(s.guild, s.app.client.ID())
	if !ok || state.ChannelID == nil {
		s.stopLocked()
		return false
	}
	s.moveVoiceLocked(*state.ChannelID)
	return true
}

func (s *session) moveVoiceLocked(channel snowflake.ID) {
	if channel == s.channel {
		return
	}
	slog.Info("voice channel moved; retaining playback", "guild", s.guild, "from", s.channel, "to", channel)
	s.channel = channel
	if dave, ok := s.app.daveClosers.Load(s.conn); ok {
		if session, ok := dave.(interface{ SetChannelID(godave.ChannelID) }); ok {
			session.SetChannelID(godave.ChannelID(channel))
		}
	}
	s.text = 0
	s.epoch++ // reject additions authorized in the former VC
	s.emptySince = time.Time{}
	if !s.idleSince.IsZero() {
		s.idleSince = time.Now()
	}
	s.moveDeadline = time.Now().Add(15 * time.Second)
	s.moveSpeaking = true
}

func (s *session) bindNotificationChannel(user, text snowflake.ID) {
	state, ok := s.app.client.Caches.VoiceState(s.guild, user)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.syncVoiceLocked()
	}
	if ok && state.ChannelID != nil && s.channel != 0 && *state.ChannelID == s.channel {
		s.text = text
	}
}
