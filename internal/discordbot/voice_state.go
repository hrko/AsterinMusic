package discordbot

import (
	"github.com/disgoorg/snowflake/v2"
	"time"
)

// Playback observes connection ownership; only the controller decides recovery.
func (s *session) syncVoiceLocked() bool {
	if c, ok := s.conn.(*voiceConnection); ok {
		id := c.ChannelID()
		if id == nil {
			if s.connecting && !c.Failed() {
				return true
			}
			s.moveVoiceLocked(0)
			s.suspendVoiceLocked()
			return false
		}
		s.moveVoiceLocked(*id)
		if c.Failed() {
			s.suspendVoiceLocked()
		}
		return true
	}
	// Test doubles do not implement connection management.
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
	s.channel = channel
	s.text = 0
	s.epoch++
	s.emptySince = time.Time{}
	if !s.idleSince.IsZero() {
		s.idleSince = time.Now()
	}
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
func (s *session) suspendVoiceLocked() {
	if s.stopped {
		return
	}
	s.recoveryHeld = true
	if s.current != nil {
		if s.currentAuto {
			copy := *s.current
			s.resumeAuto = &copy
		}
		if s.started {
			s.historyResume = &recordedPlayback{Entry: *s.current, Automatic: s.currentAuto, History: s.playbackHistory}
		}
	}
	s.stopped = true
	s.epoch++
	s.auto.invalidate()
	if s.trackCancel != nil {
		s.trackCancel()
	}
	s.idleSince = time.Now()
}
