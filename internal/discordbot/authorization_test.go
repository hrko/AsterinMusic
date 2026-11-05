package discordbot

import (
	"asterinmusic/internal/authorization"
	"asterinmusic/internal/config"
	"context"
	"errors"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAuthorizationRefreshesMemberAndFailsClosed(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagRoles, cache.FlagVoiceStates))
	c.AddGuild(discord.Guild{ID: 1, OwnerID: 9})
	c.AddRole(discord.Role{ID: 1, GuildID: 1, Permissions: discord.PermissionViewChannel})
	c.AddRole(discord.Role{ID: 5, GuildID: 1})
	voice := snowflake.ID(4)
	c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 2, ChannelID: &voice})
	body := `{"user":{"id":"2"},"roles":["5"]}`
	fail := false
	calls := 0
	transport := historyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, errors.New("PRIVATE network detail")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	svc, err := authorization.NewCedar(authorization.Config{Preset: "dj", DJRoleIDs: []string{"5"}})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{authorizer: svc, config: config.Config{GuildID: 1, ApplicationID: 3}, client: &bot.Client{Caches: c, Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
	target := authorizationTarget{guild: 1, session: "test"}
	if err := a.authorizeTarget(context.Background(), 2, authorization.PlaybackSkip, target); err != nil {
		t.Fatal(err)
	}
	body = `{"user":{"id":"2"},"roles":[]}`
	if err := a.authorizeTarget(context.Background(), 2, authorization.PlaybackSkip, target); !errors.Is(err, errPermission) {
		t.Fatal("removed role still permitted", err)
	}
	if calls != 2 {
		t.Fatal("membership was not refreshed")
	}
	fail = true
	if err := a.authorizeTarget(context.Background(), 2, authorization.QueueView, target); !errors.Is(err, errPermissionFacts) {
		t.Fatal("failed fetch did not fail closed", err)
	}
	if err := (&App{}).authorizeTarget(context.Background(), 2, authorization.QueueView, target); !errors.Is(err, errPermissionFacts) {
		t.Fatal("missing authorizer allowed")
	}
}

func TestAuthorizationOverwritePrecedence(t *testing.T) {
	member := discord.Member{GuildID: 1, User: discord.User{ID: 2}, RoleIDs: []snowflake.ID{3, 4}}
	roles := map[snowflake.ID]discord.Permissions{1: discord.PermissionViewChannel, 3: 0, 4: 0}
	for _, tc := range []struct {
		name       string
		overwrites discord.PermissionOverwrites
		visible    bool
	}{
		{"base", nil, true},
		{"everyone deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 1, Deny: discord.PermissionViewChannel}}, false},
		{"role allow wins other role deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 3, Deny: discord.PermissionViewChannel}, discord.RolePermissionOverwrite{RoleID: 4, Allow: discord.PermissionViewChannel}}, true},
		{"member deny wins role allow", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 3, Allow: discord.PermissionViewChannel}, discord.MemberPermissionOverwrite{UserID: 2, Deny: discord.PermissionViewChannel}}, false},
		{"member allow wins role deny", discord.PermissionOverwrites{discord.RolePermissionOverwrite{RoleID: 3, Deny: discord.PermissionViewChannel}, discord.MemberPermissionOverwrite{UserID: 2, Allow: discord.PermissionViewChannel}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, admin, ok := authorizationPermissions(member, 9, roles, tc.overwrites)
			if !ok || admin || p.Has(discord.PermissionViewChannel) != tc.visible {
				t.Fatal(p, admin, ok)
			}
		})
	}
	delete(roles, 4)
	if _, _, ok := authorizationPermissions(member, 9, roles, nil); ok {
		t.Fatal("missing role accepted")
	}
	roles[4] = discord.PermissionAdministrator
	if p, a, ok := authorizationPermissions(member, 9, roles, discord.PermissionOverwrites{discord.MemberPermissionOverwrite{UserID: 2, Deny: discord.PermissionViewChannel}}); !ok || !a || !p.Has(discord.PermissionViewChannel) {
		t.Fatal(p, a, ok)
	}
}

func TestCommandAuthorizationActions(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want authorization.Action
	}{
		{`{"name":"play"}`, authorization.PlaybackResume},
		{`{"name":"play","options":[{"name":"url","type":3,"value":"url"}]}`, authorization.QueueAdd},
		{`{"name":"queue"}`, authorization.QueueView},
		{`{"name":"queue","options":[{"name":"action","type":3,"value":"clear"}]}`, authorization.QueueClear},
		{`{"name":"volume"}`, authorization.VolumeView},
		{`{"name":"volume","options":[{"name":"percent","type":4,"value":30}]}`, authorization.VolumeSet},
		{`{"name":"unknown"}`, ""},
	} {
		if got := commandAction(commandData(t, tc.raw)); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}
