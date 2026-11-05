package discordbot

import (
	"asterinmusic/internal/authorization"
	"asterinmusic/internal/config"
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Existing interaction tests use real Cedar evaluation with deterministic Discord facts.
func authorizationFixture(t *testing.T, a *App) {
	t.Helper()
	if a.ctx == nil {
		a.ctx = t.Context()
	}
	if a.config.ApplicationID == 0 {
		a.config.ApplicationID = 8
	}
	if a.client == nil {
		a.client = &bot.Client{}
	}
	c := cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagRoles, cache.FlagChannels, cache.FlagVoiceStates))
	guild := a.config.GuildID
	c.AddGuild(discord.Guild{ID: guild, OwnerID: 999})
	c.AddRole(discord.Role{ID: guild, GuildID: guild, Permissions: discord.PermissionViewChannel | discord.PermissionConnect})
	if a.client.Caches != nil {
		for state := range a.client.Caches.VoiceStates(guild) {
			c.AddVoiceState(state)
			if state.ChannelID != nil {
				var channel discord.GuildVoiceChannel
				raw := `{"id":"` + state.ChannelID.String() + `","guild_id":"` + guild.String() + `","type":2}`
				if err := json.Unmarshal([]byte(raw), &channel); err != nil {
					t.Fatal(err)
				}
				c.AddChannel(channel)
			}
		}
	}
	a.client.Caches = c
	a.authorizationMember = func(_ context.Context, g, u snowflake.ID) (*discord.Member, error) {
		return &discord.Member{GuildID: g, User: discord.User{ID: u}}, nil
	}
	var err error
	a.authorizer, err = authorization.NewCedar(authorization.Config{})
	if err != nil {
		t.Fatal(err)
	}
}

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
	svc, err := authorization.NewCedar(authorization.Config{Preset: "dj_control", DJRoleIDs: []string{"5"}})
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

func TestDeferredAuthorizationDenialIsPrivate(t *testing.T) {
	for _, component := range []bool{false, true} {
		t.Run(fmt.Sprint(component), func(t *testing.T) {
			var methods []string
			transport := historyTransport(func(r *http.Request) (*http.Response, error) {
				methods = append(methods, r.Method)
				if r.Method != "DELETE" {
					var body struct {
						Content string `json:"content"`
						Flags   int    `json:"flags"`
						Embeds  []any  `json:"embeds"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if r.Method == "POST" {
						if body.Flags != 64 || body.Content != errPermission.Error() {
							t.Fatal("denial was not private")
						}
					} else if strings.Contains(body.Content, errPermission.Error()) || len(body.Embeds) != 0 {
						t.Fatal("shared response disclosed denial or metadata")
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"4","channel_id":"7"}`)), Request: r}, nil
			})
			a := &App{ctx: t.Context(), config: config.Config{ApplicationID: 8}, client: &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
			if !a.rejectDeferredAuthorization(responseRef{token: "test"}, errPermission, component) {
				t.Fatal("not handled")
			}
			want := "DELETE"
			if component {
				want = "PATCH"
			}
			if len(methods) != 2 || methods[0] != want || methods[1] != "POST" {
				t.Fatal(methods)
			}
		})
	}
}

func TestAuthorizationLogsDoNotExposeFactsOrPolicy(t *testing.T) {
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 2}}
	authorizationFixture(t, a)
	var out logBuffer
	a.logger, _ = logging.New(&out, logging.Options{Format: "json", Level: "debug"})
	var err error
	a.authorizer, err = authorization.NewCedar(authorization.Config{Policies: []authorization.Policy{{ID: "deny", Text: `// PRIVATE_POLICY
forbid(principal, action, resource);`}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.authorizeTarget(t.Context(), 1, authorization.HelpView, authorizationTarget{guild: 2}); !errors.Is(err, errPermission) {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "PRIVATE") || strings.Contains(text, `"level":"WARN"`) || !strings.Contains(text, `"allowed":false`) || !strings.Contains(text, `"policy_ids":"deny"`) {
		t.Fatal("incorrect normal denial log", text)
	}
	a.authorizationMember = func(context.Context, snowflake.ID, snowflake.ID) (*discord.Member, error) {
		return nil, errors.New("PRIVATE_HTTP")
	}
	_ = a.authorizeTarget(t.Context(), 1, authorization.HelpView, authorizationTarget{guild: 2})
	text = out.String()
	if strings.Contains(text, "PRIVATE") || !strings.Contains(text, `"level":"WARN"`) {
		t.Fatal("incorrect failure log", text)
	}
}

func TestMutationRejectsChangesDuringAuthorization(t *testing.T) {
	for _, scenario := range []string{"voice", "track", "exit", "save", "epoch", "requester", "automatic"} {
		t.Run(scenario, func(t *testing.T) {
			c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
			voice := snowflake.ID(3)
			c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
			a := &App{ctx: t.Context(), config: config.Config{GuildID: 2}, client: &bot.Client{Caches: c}}
			authorizationFixture(t, a)
			s := newSession(a, 2)
			s.channel = 3
			s.current = &music.Entry{Requester: music.Requester{ID: "1"}}
			before := s.volume.Percent()
			a.authorizationMember = func(_ context.Context, g, u snowflake.ID) (*discord.Member, error) {
				s.mu.Lock()
				switch scenario {
				case "voice":
					s.moveVoiceLocked(4)
				case "track":
					s.current = &music.Entry{ID: "new"}
				case "exit":
					a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1})
				case "save":
					s.queueSaving = make(chan struct{})
				case "epoch":
					s.epoch++
				case "requester":
					s.current.Requester.ID = "9"
				case "automatic":
					s.currentAuto = true
				}
				s.mu.Unlock()
				return &discord.Member{GuildID: g, User: discord.User{ID: u}}, nil
			}
			if err := s.controlAs(1, authorization.VolumeSet, 99); !errors.Is(err, errQueueChanged) && !errors.Is(err, errPermission) {
				t.Fatal("stale permission accepted", err)
			}
			if s.volume.Percent() != before {
				t.Fatal("mutation ran despite stale permission")
			}
		})
	}
}

func TestSelfSkipUsesCurrentManualRequester(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	voice := snowflake.ID(3)
	c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 2}, client: &bot.Client{Caches: c}}
	authorizationFixture(t, a)
	var err error
	a.authorizer, err = authorization.NewCedar(authorization.Config{Preset: authorization.PresetDJControlSelfSkip, DJRoleIDs: []string{"5"}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(a, 2)
	s.channel = 3
	s.current = &music.Entry{Requester: music.Requester{ID: "1"}}
	if err := s.checkPermission(1, authorization.PlaybackSkip); err != nil {
		t.Fatal(err)
	}
	s.currentAuto = true
	if err := s.checkPermission(1, authorization.PlaybackSkip); !errors.Is(err, errPermission) {
		t.Fatal("autoplay owner allowed", err)
	}
	s.currentAuto = false
	s.current.Requester.ID = "9"
	if err := s.checkPermission(1, authorization.PlaybackSkip); !errors.Is(err, errPermission) {
		t.Fatal("other requester allowed", err)
	}
}

func TestExtraPermitCannotBypassHistoryParticipation(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	voice := snowflake.ID(3)
	c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 2}, client: &bot.Client{Caches: c}}
	authorizationFixture(t, a)
	var err error
	a.authorizer, err = authorization.NewCedar(authorization.Config{Policies: []authorization.Policy{{ID: "wide_history", Text: `permit(principal, action == Action::"history.view", resource);`}}})
	if err != nil {
		t.Fatal(err)
	}
	a.authorizationMember = func(_ context.Context, g, u snowflake.ID) (*discord.Member, error) {
		a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: g, UserID: u})
		return &discord.Member{GuildID: g, User: discord.User{ID: u}}, nil
	}
	if _, err = a.historyVoice(2, 1); err == nil {
		t.Fatal("extra permit bypassed fixed participation after exit")
	}
}

func TestDJRolePreflight(t *testing.T) {
	for _, scenario := range []string{"present", "missing", "fetch_failed"} {
		t.Run(scenario, func(t *testing.T) {
			transport := historyTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"id":"2"}`
				status := 200
				if strings.HasSuffix(r.URL.Path, "/roles") {
					body = `[{"id":"5"}]`
					if scenario == "missing" {
						body = `[]`
					}
					if scenario == "fetch_failed" {
						status = 403
						body = `{"code":50013,"message":"PRIVATE_FAILURE"}`
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			a := &App{ctx: t.Context(), config: config.Config{GuildID: 2, ApplicationID: 8, StateDir: t.TempDir(), QueueLimit: 10, Authorization: authorization.Config{Preset: "dj_control", DJRoleIDs: []string{"5"}}}, client: &bot.Client{ApplicationID: 8, Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
			err := a.Check()
			if (err == nil) != (scenario == "present") {
				t.Fatalf("%s: %v", scenario, err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("preflight leaked REST content")
			}
		})
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
