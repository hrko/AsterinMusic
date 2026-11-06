package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"context"

	"github.com/disgoorg/snowflake/v2"
)

const failureNotificationCapacity = 16

type failureNotification struct {
	channel  snowflake.ID
	sequence uint64
	track    *music.Track
	text     string
	terminal bool
}

// A single worker owns REST notifications for the session. Playback completion
// never waits for Discord; on congestion the latest outcomes replace oldest.
func (s *session) enqueueFailure(channel snowflake.ID, track *music.Track, text string, terminal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel == 0 || channel != s.text || s.shuttingDown || s.app.ctx.Err() != nil {
		return
	}
	if s.failureQueue == nil {
		s.failureQueue = make(chan failureNotification, failureNotificationCapacity)
		s.failureWorkerDone = make(chan struct{})
		ctx, cancel := context.WithCancel(s.app.ctx)
		s.failureWorkerCancel = cancel
		go s.runFailureNotifications(ctx, s.failureQueue, s.failureWorkerDone)
	}
	n := failureNotification{channel: channel, sequence: s.playbackSequence, text: text, terminal: terminal}
	if track != nil {
		copy := *track
		n.track = &copy
	}
	select {
	case s.failureQueue <- n:
	default:
		select {
		case <-s.failureQueue:
			s.failureNotificationsDropped++
		default:
		}
		s.failureQueue <- n // Producers are serialized by mu; space is available.
	}
}

func (s *session) runFailureNotifications(ctx context.Context, queue <-chan failureNotification, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-queue:
			if ctx.Err() != nil {
				return
			}
			s.publishFailure(ctx, n.channel, n.track, n.text, n.terminal, &n.sequence)
			s.mu.Lock()
			dropped := s.failureNotificationsDropped
			s.failureNotificationsDropped = 0
			s.mu.Unlock()
			if dropped != 0 {
				logging.OrDiscard(s.logger).WarnContext(ctx, "playback notifications coalesced", "event", "notification.playback_failure.overflow", "suppressed", dropped)
			}
		}
	}
}
