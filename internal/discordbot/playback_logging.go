package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"log/slog"
	"time"
)

type playbackStart struct {
	historyFailed bool
	channel       string
	at            time.Time
}

// A track owns one buffered start notification and one observer. Audio sends
// never wait on a Writer. The playback owner joins the observer before moving
// to another track, so a stalled Writer cannot accumulate logging goroutines.
type playbackLog struct {
	logger  *slog.Logger
	started chan playbackStart
	done    chan struct{}
	joined  chan struct{}
}

func newPlaybackLog(ctx context.Context, logger *slog.Logger, id string, automatic bool) *playbackLog {
	p := &playbackLog{logger: logging.OrDiscard(logger).With("playback_id", id), started: make(chan playbackStart, 1), done: make(chan struct{}), joined: make(chan struct{})}
	go func() {
		defer close(p.joined)
		emit := func(s playbackStart) {
			p.logger.InfoContext(ctx, "audio playback started", "event", "playback.started", "channel_id", s.channel, "automatic", automatic, "audio_started_at", s.at)
			if s.historyFailed {
				logging.Component(p.logger, "history").ErrorContext(ctx, "history enqueue failed", "event", "history.queue_full.failed", "reason", "queue_unavailable", "action", "persistence_failed")
			}
		}
		select {
		case s := <-p.started:
			emit(s)
		case <-p.done:
			select {
			case s := <-p.started:
				emit(s)
			default:
			}
		}
	}()
	return p
}

func (p *playbackLog) join() { close(p.done); <-p.joined }
func (p *playbackLog) finish(ctx context.Context, level slog.Level, reason, action string, position time.Duration) {
	p.logger.Log(ctx, level, "playback ended", "event", "playback.ended", "reason", reason, "action", action, "position_ms", position.Milliseconds())
}
