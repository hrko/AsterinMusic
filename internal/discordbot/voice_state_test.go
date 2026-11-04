package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"asterinmusic/internal/audio"
	"asterinmusic/internal/history"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func TestMovedVoiceCacheChangesAuthorizationAndNotification(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates, cache.FlagMembers))
	c.SetSelfUser(discord.OAuth2User{User: discord.User{ID: 10, Bot: true}})
	a := &App{ctx: context.Background(), client: &bot.Client{Caches: c}}
	s := newSession(a, 1)
	s.conn = &retainedVoiceConn{}
	s.channel = 3
	s.text = 7
	next, old := snowflake.ID(4), snowflake.ID(3)
	c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 10, ChannelID: &next})
	c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 11, ChannelID: &old})
	c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 12, ChannelID: &next})
	if _, err := s.authorize(11); err == nil {
		t.Fatal("old VC still authorized")
	}
	if s.channel != 4 || s.text != 0 {
		t.Fatal("scope not synchronized")
	}
	if channel, err := s.authorize(12); err != nil || channel != 4 {
		t.Fatal(channel, err)
	}
	s.bindNotificationChannel(11, 8)
	if s.text != 0 {
		t.Fatal("old user rebound notifications")
	}
	s.bindNotificationChannel(12, 9)
	if s.text != 9 {
		t.Fatal("new VC user cannot bind notifications")
	}
	s.emptySince = time.Now().Add(-time.Hour)
	s.tick()
	if s.conn == nil || !s.emptySince.IsZero() {
		t.Fatal("human in destination not respected")
	}
}

type retainedVoiceConn struct {
	voice.Conn
	closed bool
}

func (c *retainedVoiceConn) Close(context.Context) { c.closed = true }

func TestThreeManualFailuresKeepVoiceAndPendingQueue(t *testing.T) {
	s := autoSession()
	conn := &retainedVoiceConn{}
	s.conn = conn
	s.channel = 3
	tracks := []music.Track{autoTrack("aaaaaaaaaaa"), autoTrack("bbbbbbbbbbb"), autoTrack("ccccccccccc"), autoTrack("ddddddddddd")}
	s.queue.Add(music.Requester{ID: "user"}, tracks, "", "")
	calls := 0
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error { calls++; return errors.New("source failed") }
	s.loop(make(chan struct{}))
	_, pending := s.queue.Snapshot()
	if calls != 3 || len(pending) != 1 || pending[0].Track.URL != tracks[3].URL || s.conn != conn || s.channel != 3 || conn.closed || s.running || !s.stopped || s.auto.enabled {
		t.Fatal("failure stop lost voice or pending work")
	}
	if s.idleSince.IsZero() || s.shouldDisconnect(time.Now().Add(time.Hour)) {
		t.Fatal("failure stop schedules default idle disconnect")
	}
	// Restart scheduling with the existing connection; counter must start fresh.
	s.queue.Add(music.Requester{ID: "user"}, tracks[:1], "", "")
	s.stopped = false
	s.running = true
	calls = 0
	s.playAudio = func(context.Context, voice.Conn, audio.Producer) error { calls++; return errors.New("again") }
	s.loop(make(chan struct{}))
	if calls != 2 || s.conn != conn {
		t.Fatal("failure counter survived restart")
	}
}

func TestMoveRetainsPlaybackAndResetsOnlyScope(t *testing.T) {
	s := autoSession()
	conn := &retainedVoiceConn{}
	s.conn = conn
	s.channel = 3
	s.text = 7
	s.position = 42 * time.Second
	s.started = true
	s.current = &music.Entry{Track: autoTrack("abcdefghijk")}
	s.playbackHistory = &history.Record{VoiceID: "3", OccurrenceID: "first"}
	s.queue.Add(music.Requester{ID: "user"}, []music.Track{autoTrack("bbbbbbbbbbb")}, "", "")
	s.emptySince = time.Now().Add(-time.Hour)
	oldEpoch, revision, entry := s.epoch, s.auto.revision, s.current
	s.moveVoiceLocked(4)
	_, pending := s.queue.Snapshot()
	if s.channel != 4 || s.text != 0 || !s.emptySince.IsZero() || s.epoch != oldEpoch+1 || s.auto.revision != revision || !s.auto.enabled || s.current != entry || !s.started || s.position != 42*time.Second || s.conn != conn || conn.closed || len(pending) != 1 || s.playbackHistory.VoiceID != "3" {
		t.Fatal("move changed playback or retained old scope")
	}
	s.notifyFailure(7, &entry.Track, "must not send to old channel", false) // no REST client: a leak would panic

	s.moveVoiceLocked(4)
	if s.epoch != oldEpoch+1 {
		t.Fatal("duplicate event reset transition")
	}
}
