package discordbot

import (
	"encoding/json"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"testing"
)

func TestChannelBitrateUsesLatestCache(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagChannels))
	s := newSession(&App{client: &bot.Client{Caches: c}}, 1)
	s.channel = 2
	if s.channelBitrate() != 0 {
		t.Fatal("missing cache")
	}
	for _, raw := range []string{`{"id":"2","guild_id":"1","type":2,"bitrate":64000}`, `{"id":"2","guild_id":"1","type":2,"bitrate":128000}`} {
		var ch discord.GuildVoiceChannel
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		c.AddChannel(ch)
		if got := s.channelBitrate(); got != ch.Bitrate() {
			t.Fatal(got)
		}
	}
	s.channel = 3
	if s.channelBitrate() != 0 {
		t.Fatal("read wrong channel")
	}
}
