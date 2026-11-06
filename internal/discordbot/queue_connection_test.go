package discordbot

import (
	"asterinmusic/internal/config"
	"asterinmusic/internal/music"
	"errors"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func TestQueueAddWhileVoicePreparationDoesNotOpenAgain(t *testing.T) {
	for _, phase := range []string{"initial", "reopen", "recovering", "closing", "old_loop"} {
		t.Run(phase, func(t *testing.T) {
			a := &App{ctx: t.Context(), config: config.Config{GuildID: 2, QueueLimit: 2}}
			channel := snowflake.ID(3)
			a.client = &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagVoiceStates))}
			a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &channel})
			authorizationFixture(t, a)
			s := newSession(a, 2)
			s.channel = channel
			s.createConn = func() voice.Conn { t.Error("parallel connection created"); return nil }
			switch phase {
			case "initial", "reopen":
				s.connecting = true
			case "recovering":
				c := newVoiceConnection(2, 8)
				c.channel, c.phase = channel, voiceRecovering
				s.conn, s.running = c, true
			case "closing":
				s.closing = make(chan struct{})
			case "old_loop":
				s.stopped, s.running = true, true
			}
			tracks := []music.Track{autoTrack("aaaaaaaaaaa"), autoTrack("bbbbbbbbbbb")}
			who := music.Requester{ID: "1"}
			if n, _, err := s.acceptAt(1, who, tracks, "", "", 4, s.version(), music.Insertion{}); err != nil || n != 2 {
				t.Fatalf("queue add rejected: n=%d err=%v", n, err)
			}
			if n, _, err := s.acceptAt(1, who, tracks[:1], "", "", 4, s.version(), music.Insertion{}); !errors.Is(err, errQueueFull) || n != 0 {
				t.Fatalf("capacity bypassed: n=%d err=%v", n, err)
			}
			other := snowflake.ID(5)
			a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &other})
			if _, _, err := s.acceptAt(1, who, tracks[:1], "", "", 4, s.version(), music.Insertion{}); err == nil {
				t.Fatal("caller outside target VC was allowed")
			}
			_, pending := s.queue.Snapshot()
			if len(pending) != 2 {
				t.Fatal("accepted queue was changed")
			}
		})
	}
}
