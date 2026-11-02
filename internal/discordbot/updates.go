package discordbot

import (
	"asterinmusic/internal/updater"
	"log/slog"
	"time"
)

func (a *App) checkUpdates() {
	m := updater.New()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if v, err := m.Latest(a.ctx); err == nil {
			slog.Info("yt-dlp release check", "latest", v, "apply", "mise run update-apply -- VERSION SAMPLE_URL")
		} else if a.ctx.Err() == nil {
			slog.Warn("yt-dlp update check failed; retaining current executable")
		}
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}
