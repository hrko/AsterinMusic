package discordbot

import (
	"asterinmusic/internal/authorization"
	"github.com/disgoorg/snowflake/v2"
)

func (s *session) controlAs(user snowflake.ID, action authorization.Action, percent int) error {
	if err := s.lockAuthorized(user, action); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	switch action {
	case authorization.VolumeSet:
		return s.volume.Set(percent)
	case authorization.PlaybackStop:
		s.stopLocked()
		return nil
	case authorization.PlaybackSkip:
		if s.trackCancel == nil {
			return errNotPlaying
		}
		s.skipping = true
		s.trackCancel()
		return nil
	default:
		return errPermission
	}
}

// Check at mutation time under mu, not only at command receipt.
func (s *session) mutableLocked() error {
	if s.shuttingDown {
		return errRestarting
	}
	return nil
}

func (s *session) setVolume(percent int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	return s.volume.Set(percent)
}
