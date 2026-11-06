package discordbot

import (
	"context"
	"testing"

	"asterinmusic/internal/authorization"
	"asterinmusic/internal/config"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestQueueEditAuthorizationAtMutation(t *testing.T) {
	for _, scenario := range []string{"own", "other", "left", "consumed", "shutdown", "forbid", "remote_permit"} {
		t.Run(scenario, func(t *testing.T) {
			voice := snowflake.ID(4)
			c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
			c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 2, ChannelID: &voice})
			a := &App{ctx: t.Context(), config: config.Config{GuildID: 1, ApplicationID: 8}, client: &bot.Client{Caches: c}}
			authorizationFixture(t, a)
			cfg := authorization.Config{Preset: authorization.PresetDJControl, DJRoleIDs: []string{"5"}}
			if scenario == "forbid" {
				cfg.Policies = []authorization.Policy{{ID: "deny", Text: `forbid(principal, action == Action::"queue.remove", resource);`}}
			}
			if scenario == "remote_permit" {
				cfg.Policies = []authorization.Policy{{ID: "allow", Text: `permit(principal, action, resource);`}}
			}
			var err error
			a.authorizer, err = authorization.NewCedar(cfg)
			if err != nil {
				t.Fatal(err)
			}
			s := newSession(a, 1)
			s.accessVoice = voice
			owner := "2"
			if scenario == "other" {
				owner = "3"
			}
			s.queue.Add(music.Requester{ID: owner}, []music.Track{{URL: "one"}}, "", "")
			id := s.queue.RequesterEntries(owner)[0].ID
			original := a.authorizationMember
			a.authorizationMember = func(ctx context.Context, guild, user snowflake.ID) (*discord.Member, error) {
				switch scenario {
				case "left", "remote_permit":
					a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 2})
				case "consumed":
					s.queue.Start()
				case "shutdown":
					s.shuttingDown = true
				}
				return original(ctx, guild, user)
			}
			err = s.editQueueAs(2, owner, id, 0, nil, true)
			if (err == nil) != (scenario == "own") {
				t.Fatalf("unexpected result: %v", err)
			}
			if scenario != "own" && scenario != "consumed" && len(s.queue.RequesterEntries(owner)) != 1 {
				t.Fatal("denied mutation changed queue")
			}
		})
	}
}
