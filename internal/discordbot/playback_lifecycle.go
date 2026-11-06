package discordbot

import (
	"context"
	"time"
)

// running=false can precede the old loop's final cleanup. Do not replace done
// or prepare a new restore observation until that owner has actually finished.
// The caller holds mu on entry and return; cancellation and status stay usable.
func (s *session) waitPreviousPlaybackLocked() error {
	if s.done == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	if s.running && !s.stopped {
		return errConnecting
	}
	previous, epoch := s.done, s.epoch
	ctx, cancel := context.WithTimeout(s.app.ctx, 5*time.Second)
	s.connecting, s.connectCancel = true, cancel
	s.mu.Unlock()
	var err error
	select {
	case <-previous:
	case <-ctx.Done():
		err = errStopping
	}
	cancel()
	s.mu.Lock()
	s.connecting, s.connectCancel = false, nil
	if s.epoch != epoch || s.shuttingDown || s.app.ctx.Err() != nil {
		return errQueueChanged
	}
	if err != nil {
		return err
	}
	return s.mutableLocked()
}
