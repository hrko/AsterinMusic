package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/updater"
	"time"
)

func (a *App) checkUpdates() {
	m := updater.New()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if v, err := m.Latest(a.ctx); err == nil {
			logging.Component(a.logger, "updater").InfoContext(a.ctx, "yt-dlp release check", "event", "updater.check.completed", "latest", v)
		} else if a.ctx.Err() == nil {
			logging.Component(a.logger, "updater").WarnContext(a.ctx, "yt-dlp update check failed", "event", "updater.check.failed", "reason", failureClass(err), "action", "retain_current_executable")
		}
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}
