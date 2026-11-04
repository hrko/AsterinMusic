package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"time"
)

// Policy accepts an account's entire configured and observed guild set, not
// just guilds currently playing. Shared accounts never publish activity.
func activityTitle(enabled bool, configured, joined []snowflake.ID, guild snowflake.ID, playing bool, title string) string {
	if !enabled || len(configured) != 1 || len(joined) != 1 || configured[0] == 0 || configured[0] != joined[0] || guild != configured[0] || !playing {
		return ""
	}
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 128 {
		title = string(runes[:127]) + "…"
	}
	return title
}

func (a *App) activitySnapshot() string {
	// Unknown/unavailable membership must never authorize a disclosure.
	if len(a.client.Caches.UnreadyGuildIDs()) > 0 || len(a.client.Caches.UnavailableGuildIDs()) > 0 {
		return ""
	}
	var joined []snowflake.ID
	for guild := range a.client.Caches.Guilds() {
		joined = append(joined, guild.ID)
	}
	a.mu.Lock()
	s := a.sessions[a.config.GuildID]
	a.mu.Unlock()
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	title := ""
	if s.current != nil {
		title = s.current.Track.Title
	}
	playing := s.current != nil && s.started && s.running && !s.stopped && !s.skipping && !s.shuttingDown && s.conn != nil
	return activityTitle(a.config.ActivityEnabled, []snowflake.ID{a.config.GuildID}, joined, s.guild, playing, title)
}

// Single owner of network updates. Recompute instead of queueing old titles.
type presencePublisher struct {
	sent     bool
	last     string
	revision uint64
	next     time.Time
}

func (p *presencePublisher) update(ctx context.Context, now time.Time, revision uint64, snapshot func() string, send func(context.Context, string) error) error {
	if now.Before(p.next) {
		return nil
	}
	title := snapshot()
	if p.sent && p.last == title && p.revision == revision {
		return nil
	}
	p.next = now.Add(5 * time.Second)
	if err := send(ctx, title); err != nil {
		return err
	}
	p.sent = true
	p.last = title
	p.revision = revision
	return nil
}

func (a *App) publishActivity(ctx context.Context, title string) error {
	return a.client.SetPresence(ctx, func(p *gateway.MessageDataPresenceUpdate) {
		*p = gateway.MessageDataPresenceUpdate{Status: discord.OnlineStatusOnline, Activities: []discord.Activity{}}
		if title != "" {
			p.Activities = append(p.Activities, discord.Activity{Type: discord.ActivityTypeListening, Name: title})
		}
	})
}

func (a *App) runPresence() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	p := presencePublisher{}
	var failures logging.Repetition
	logger := logging.Component(a.logger, "app")
	defer failures.Close(context.Background(), logger)
	for {
		if a.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
		_ = p.update(ctx, time.Now(), a.presenceRevision.Load(), a.activitySnapshot, func(ctx context.Context, title string) error {
			err := a.publishActivity(ctx, title)
			if err == nil {
				failures.ResolveAll(ctx, logger)
			} else if a.ctx.Err() == nil {
				failures.Failure(ctx, logger, failureClass(err), "activity update failed; will retry", "event", "app.presence.failed", "reason", failureClass(err), "action", "retry")
			}
			return err
		})
		cancel()
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
