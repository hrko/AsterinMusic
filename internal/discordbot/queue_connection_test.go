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

// Used while the real handshake is stalled at each wire stage. Session flags
// describe the existing start owner; the controller's attempt must stay intact.
func assertQueueAdditionSharesHandshake(t *testing.T, c *voiceConnection) {
	t.Helper()
	channel := snowflake.ID(3)
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 1, QueueLimit: 2}, client: &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagVoiceStates))}}
	a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 9, ChannelID: &channel})
	authorizationFixture(t, a)
	s := newSession(a, 1)
	s.conn, s.channel, s.connecting = c, channel, true
	s.createConn = func() voice.Conn { t.Error("addition created another connection"); return nil }
	c.mu.Lock()
	generation, attempt := c.generation, c.traceAttempt
	c.mu.Unlock()
	for _, front := range []bool{false, true} {
		n, _, err := s.acceptAt(9, music.Requester{ID: "9"}, []music.Track{autoTrack("aaaaaaaaaaa")}, "", "", 4, s.version(), music.Insertion{Front: front})
		if err != nil || n != 1 {
			t.Fatalf("addition during handshake: n=%d err=%v", n, err)
		}
	}
	c.mu.Lock()
	unchanged := c.generation == generation && c.traceAttempt == attempt && c.phase == voiceConnecting
	c.mu.Unlock()
	_, pending := s.queue.Snapshot()
	if !unchanged || len(pending) != 2 || s.running {
		t.Fatal("addition replaced handshake or started a parallel playback loop")
	}
}
