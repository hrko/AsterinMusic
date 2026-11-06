package discordbot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"asterinmusic/internal/authorization"
	"asterinmusic/internal/config"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestQueueEditorRechecksRoleAndFailedFacts(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a, s, v := queueEditorApp(t)
			s.queue.Add(music.Requester{ID: "3"}, []music.Track{{URL: "other"}}, "", "")
			var err error
			a.authorizer, err = authorization.NewCedar(authorization.Config{Preset: authorization.PresetDJOnly, DJRoleIDs: []string{"5"}})
			if err != nil {
				t.Fatal(err)
			}
			a.client.Caches.AddRole(discord.Role{ID: 5, GuildID: 1})
			a.authorizationMember = func(context.Context, snowflake.ID, snowflake.ID) (*discord.Member, error) {
				return &discord.Member{User: discord.User{ID: 2}, RoleIDs: []snowflake.ID{5}}, nil
			}
			if err = a.loadQueueEditor(v, false); err != nil {
				t.Fatal(err)
			}
			v.selected = v.entries[0].ID
			a.authorizationMember = func(context.Context, snowflake.ID, snowflake.ID) (*discord.Member, error) {
				if failed {
					return nil, errors.New("private upstream failure")
				}
				return &discord.Member{User: discord.User{ID: 2}}, nil
			}
			_ = a.mutateQueueEditor(v, 0, true)
			if len(s.queue.RequesterEntries("3")) != 1 {
				t.Fatal("stale display permission was used")
			}
		})
	}
}

func TestQueueEditorBusyReopenAndConsumedSelection(t *testing.T) {
	a, s, v := queueEditorApp(t)
	s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "one"}, {URL: "two"}}, "", "")
	if err := a.loadQueueEditor(v, false); err != nil {
		t.Fatal(err)
	}
	a.rememberQueueEditor(v)
	v.mu.Lock()
	_, _, err := a.queueEditorAction(v.componentID("remove"), 2, &v.guild, 5)
	v.mu.Unlock()
	if err == nil {
		t.Fatal("busy view accepted another action")
	}
	v.selected = v.entries[0].ID
	s.queue.Start()
	if err = a.mutateQueueEditor(v, 0, true); err != nil {
		t.Fatal(err)
	}
	if v.selected != "" || v.notice != "この曲は待機キューにありません。一覧を更新しました。" {
		t.Fatal(v.notice)
	}
	newView := &queueEditor{id: "replacement", owner: v.owner, guild: v.guild, message: v.message, expires: v.expires}
	a.rememberQueueEditor(newView)
	if _, _, err = a.queueEditorAction(v.componentID("refresh"), 2, &v.guild, 5); err == nil {
		t.Fatal("old message generation survived reopening")
	}
}

func queueEditorApp(t *testing.T) (*App, *session, *queueEditor) {
	voice := snowflake.ID(4)
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	c.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 2, ChannelID: &voice})
	a := &App{ctx: t.Context(), config: config.Config{GuildID: 1, ApplicationID: 8}, client: &bot.Client{Caches: c}}
	authorizationFixture(t, a)
	s := newSession(a, 1)
	s.accessVoice = voice
	a.sessions = map[snowflake.ID]*session{1: s}
	v := &queueEditor{id: "view", owner: 2, guild: 1, voice: voice, message: 5, sessionEpoch: s.epoch, expires: time.Now().Add(queueEditorTTL), page: 1, ownerPage: 1}
	return a, s, v
}

func TestQueueEditorOwnerDisplayNames(t *testing.T) {
	a, _, v := queueEditorApp(t)
	a.client.Caches = cache.New(cache.WithCaches(cache.FlagMembers))
	nick, global := "サーバーの名前", "表示名"
	a.client.Caches.AddMember(discord.Member{GuildID: 1, Nick: &nick, User: discord.User{ID: 2, Username: "account", GlobalName: &global}})
	a.client.Caches.AddMember(discord.Member{GuildID: 1, User: discord.User{ID: 3, Username: "account", GlobalName: &global}})
	a.client.Caches.AddMember(discord.Member{GuildID: 1, User: discord.User{ID: 4, Username: "username"}})
	for _, id := range []string{"2", "3", "4", "5"} {
		v.owners = append(v.owners, queueEditOwner{requester: music.Requester{ID: id, Name: "保存済み"}, count: 1})
	}
	a.queueEditorOwnerNames(v)
	for i, want := range []string{nick, global, "username", "保存済み"} {
		if v.owners[i].requester.Name != want || v.owners[i].requester.ID != fmt.Sprint(i+2) {
			t.Fatalf("owner %d: %+v", i, v.owners[i])
		}
	}
}

func TestQueueEditorLoadsPerItemPermissionsWithOneLookup(t *testing.T) {
	a, s, v := queueEditorApp(t)
	s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "one"}, {URL: "two"}}, "", "")
	s.queue.Add(music.Requester{ID: "3"}, []music.Track{{URL: "other"}}, "", "")
	var err error
	a.authorizer, err = authorization.NewCedar(authorization.Config{Preset: authorization.PresetDJControl, DJRoleIDs: []string{"5"}, Policies: []authorization.Policy{{ID: "item", Text: `forbid(principal, action == Action::"queue.remove", resource) when {resource.entry == "1"};`}}})
	if err != nil {
		t.Fatal(err)
	}
	load := a.authorizationMember
	calls := 0
	a.authorizationMember = func(ctx context.Context, g, u snowflake.ID) (*discord.Member, error) { calls++; return load(ctx, g, u) }
	if err = a.loadQueueEditor(v, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(v.owners) != 1 || len(v.entries) != 2 || v.permissions["1"].remove || !v.permissions["1"].reorder || !v.permissions["2"].remove || v.permissions["3"].remove {
		t.Fatalf("unexpected display permissions: %+v calls=%d", v.permissions, calls)
	}
	v.selected = "2"
	if err = a.mutateQueueEditor(v, 1, false); err != nil {
		t.Fatal(err)
	}
	if v.entries[0].ID != "2" || v.selected != "2" || v.notice == "" {
		t.Fatal("move not reflected")
	}
	if err = a.mutateQueueEditor(v, 0, true); err != nil {
		t.Fatal(err)
	}
	if v.selected != "" || len(v.entries) != 1 {
		t.Fatal("delete selected replacement")
	}
}

func TestQueueEditorConflictAndScope(t *testing.T) {
	a, s, v := queueEditorApp(t)
	s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "one"}, {URL: "two"}}, "", "")
	if err := a.loadQueueEditor(v, false); err != nil {
		t.Fatal(err)
	}
	v.selected = v.entries[1].ID
	s.queue.Start()
	if err := a.mutateQueueEditor(v, 1, false); err != nil {
		t.Fatal(err)
	}
	if v.notice != "待機曲の順番が変わりました。一覧を確認して操作し直してください。" {
		t.Fatal(v.notice)
	}
	a.rememberQueueEditor(v)
	locked, _, err := a.queueEditorAction(v.componentID("refresh"), 2, &v.guild, 5)
	if err != nil {
		t.Fatal(err)
	}
	locked.mu.Unlock()
	if _, _, err = a.queueEditorAction(v.componentID("refresh"), 3, &v.guild, 5); err == nil {
		t.Fatal("wrong owner")
	}
	v.revision++
	if _, _, err = a.queueEditorAction("qedit:view:0:refresh", 2, &v.guild, 5); err == nil {
		t.Fatal("stale revision")
	}
	s.epoch++
	if err = a.loadQueueEditor(v, false); err == nil {
		t.Fatal("changed session accepted")
	}
}
