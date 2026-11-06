package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"context"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"sync"
	"time"
)

// One notification per uninterrupted failure series in the same channel.
// A successfully sent audio packet advances playbackSequence and starts a new series.
type failureNotice struct {
	mu               sync.Mutex
	sequence         uint64
	channel, message snowflake.ID
}

func (n *failureNotice) publish(sequence uint64, channel snowflake.ID, embed discord.Embed, send func(snowflake.ID, discord.Embed) (snowflake.ID, error)) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sequence != sequence || n.channel != channel {
		n.message = 0
		n.sequence = sequence
		n.channel = channel
	}
	id, err := send(n.message, embed)
	if err != nil {
		return err
	}
	n.message = id
	return nil
}
func (s *session) notifyFailure(channel snowflake.ID, track *music.Track, text string, terminal bool) {
	s.publishFailure(s.app.ctx, channel, track, text, terminal, nil)
}

func (s *session) publishFailure(parent context.Context, channel snowflake.ID, track *music.Track, text string, terminal bool, expected *uint64) {
	if channel == 0 {
		return
	}
	s.mu.Lock()
	if channel != s.text || (expected != nil && *expected != s.playbackSequence) {
		s.mu.Unlock()
		return
	}
	sequence := s.playbackSequence
	s.mu.Unlock()
	embed := discord.Embed{Title: "曲を再生できませんでした", Description: text, Color: colorWarning}
	if terminal {
		embed.Title = "再生を停止しました"
		if strings.Contains(text, "OFF") {
			embed.Title = "自動再生をOFFにしました"
		}
		embed.Color = colorError
	}
	if track != nil {
		embed.Description = singleLink(*track) + "\n\n" + text
	}
	operation := "create"
	err := s.failureNotice.publish(sequence, channel, embed, func(id snowflake.ID, e discord.Embed) (snowflake.ID, error) {
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()
		if id != 0 {
			operation = "update"
			m, err := s.app.client.Rest.UpdateMessage(channel, id, messageUpdate("", &e, nil), rest.WithCtx(ctx))
			if err == nil {
				return m.ID, nil
			}
			if !rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownMessage) {
				return 0, err
			}
		}
		operation = "create"
		m, err := s.app.client.Rest.CreateMessage(channel, discord.MessageCreate{Embeds: []discord.Embed{e}, AllowedMentions: &discord.AllowedMentions{}}, rest.WithCtx(ctx))
		if err != nil {
			return 0, err
		}
		return m.ID, nil
	})
	logFailure(s.app.ctx, logging.OrDiscard(s.logger).With("notification_channel_id", channel.String(), "notification_operation", operation), "notification.playback_failure", err)
}
