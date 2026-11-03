package discordbot

// All callers hold mu. Waiters release it so audio/status/cancellation can
// proceed. After waiting, callers must recheck their mutation preconditions.
func (s *session) waitQueueLocked() {
	for s.queueSaving != nil {
		done := s.queueSaving
		s.mu.Unlock()
		<-done
		s.mu.Lock()
	}
}

// Called after waitQueueLocked and authorization/state checks, without an
// intervening unlock. The callback must access only the queue, not session state.
func (s *session) queueIOLocked(operation func()) {
	done := make(chan struct{})
	s.queueSaving = done
	s.mu.Unlock()
	operation()
	s.mu.Lock()
	s.queueSaving = nil
	close(done)
}
