package discordbot

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
