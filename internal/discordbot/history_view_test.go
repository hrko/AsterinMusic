package discordbot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"asterinmusic/internal/config"
	"asterinmusic/internal/history"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestHistoryActionUsesCurrentVoiceAndRejectsConcurrentClicks(t *testing.T) {
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	a := &App{config: config.Config{GuildID: 2}, client: &bot.Client{Caches: c}}
	v := &historyView{id: "view", owner: 1, guild: 2, voice: 3, message: 4, revision: 5, expires: time.Now().Add(time.Minute)}
	if !a.rememberHistory(v) {
		t.Fatal("remember")
	}
	voice := snowflake.ID(3)
	c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
	authorizationFixture(t, a)
	c = a.client.Caches
	got, action, err := a.historyAction(v.componentID("next"), 1, &v.guild, 4)
	if err != nil || got != v || action != "next" {
		t.Fatalf("valid action: %v", err)
	}
	if _, _, err := a.historyAction(v.componentID("next"), 1, &v.guild, 4); err == nil {
		t.Fatal("concurrent click accepted")
	}
	got.mu.Unlock()
	voice = 9
	c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
	if _, _, err := a.historyAction(v.componentID("next"), 1, &v.guild, 4); err == nil {
		t.Fatal("moved user accepted")
	}
	c.RemoveVoiceState(2, 1)
	if _, _, err := a.historyAction(v.componentID("refresh"), 1, &v.guild, 4); err == nil {
		t.Fatal("departed user accepted")
	}
	if _, _, err := a.historyAction("history:view:5:next", 1, nil, 4); err == nil {
		t.Fatal("DM accepted")
	}
}

func TestHistoryViewAccessBoundary(t *testing.T) {
	now := time.Now()
	v := historyView{owner: 1, guild: 2, voice: 3, message: 4, revision: 5, expires: now.Add(time.Minute)}
	if !v.valid(1, 2, 3, 4, 5, now) {
		t.Fatal("valid view rejected")
	}
	for i := range 5 {
		values := []uint64{1, 2, 3, 4, 5}
		values[i]++
		if v.valid(snowflake.ID(values[0]), snowflake.ID(values[1]), snowflake.ID(values[2]), snowflake.ID(values[3]), values[4], now) {
			t.Fatalf("accepted mismatched boundary %d", i)
		}
	}
	if v.valid(1, 2, 3, 4, 5, v.expires) {
		t.Fatal("expired view accepted")
	}
}

func TestHistoryViewLayoutAndControls(t *testing.T) {
	for _, page := range []int{1, 2, 10, 1000} {
		v := historyView{id: "view", voice: 3, page: page, snapshot: history.Snapshot{Count: 10000}}
		for i := range 10 {
			track := autoTrack(fmt.Sprintf("%011d", i))
			track.Title = strings.Repeat("日本語😀[title]", 30)
			track.ChannelTitle = strings.Repeat("channel", 20)
			track.ChannelID = "UCabcdefghijklmnopqrstuv"
			v.rows = append(v.rows, history.Record{ID: int64(i + 1), StartedAt: time.Unix(100, 0), Automatic: i == 1, Entry: music.Entry{Track: track, Requester: music.Requester{ID: "123"}}})
		}
		e, components := v.render()
		indent := strings.Repeat(" ", len(fmt.Sprint((page-1)*10+1))+2)
		if strings.Count(e.Description, "\n"+indent+"-# <t:100:R>") != 10 || !strings.Contains(e.Description, "<@123>") || !strings.Contains(e.Description, "自動再生") {
			t.Fatalf("page %d: invalid metadata or indentation", page)
		}
		if e.Thumbnail != nil || e.Image != nil || len(utf16.Encode([]rune(e.Description))) > 4096 {
			t.Fatal("embed exceeds display constraints")
		}
		if len(components) != 3 {
			t.Fatal("expected navigation, selection and add rows")
		}
		nav := components[0].(discord.ActionRowComponent).Components
		if len(nav) != 4 || nav[1].(discord.ButtonComponent).Label != fmt.Sprintf("%d / 1000", page) {
			t.Fatal("page control is not the expected button")
		}
		if nav[0].(discord.ButtonComponent).Disabled != (page == 1) || nav[2].(discord.ButtonComponent).Disabled != (page == 1000) {
			t.Fatal("page bounds")
		}
		if !v.selectRecord(2) || v.selectRecord(99) || v.selected != 2 {
			t.Fatal("selection escaped displayed records")
		}
	}
	message := ephemeral("test")
	if message.Flags != discord.MessageFlagEphemeral || message.AllowedMentions == nil || len(message.AllowedMentions.Parse) != 0 {
		t.Fatal("history response is public or can ping")
	}
}

func TestHistoryViewSnapshotAndSelection(t *testing.T) {
	store, err := history.Open(filepath.Join(t.TempDir(), "history.sqlite"), 11)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	appendRecord := func(i int) {
		t.Helper()
		err := store.Append(ctx, history.Record{OccurrenceID: fmt.Sprint(i), GuildID: "1", VoiceID: "2", StartedAt: time.Unix(int64(i), 0), Entry: music.Entry{Track: autoTrack("12345678901")}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 11 {
		appendRecord(i)
	}
	snap, err := store.Snapshot(ctx, "1", "2")
	if err != nil {
		t.Fatal(err)
	}
	v := historyView{}
	if err := v.load(ctx, store, snap, 1); err != nil {
		t.Fatal(err)
	}
	if !v.selectRecord(v.rows[0].ID) {
		t.Fatal("select")
	}
	if err := v.load(ctx, store, snap, 2); err != nil {
		t.Fatal(err)
	}
	if v.selected != 0 || len(v.rows) != 1 {
		t.Fatal("page movement retained selection")
	}
	id := v.rows[0].ID
	if err := v.load(ctx, store, snap, 3); !errors.Is(err, history.ErrPage) {
		t.Fatal(err)
	}
	if v.page != 2 || v.rows[0].ID != id {
		t.Fatal("invalid page replaced view")
	}
	appendRecord(12)
	if err := v.load(ctx, store, snap, 2); !errors.Is(err, history.ErrExpired) {
		t.Fatal(err)
	}
	if v.rows[0].ID != id {
		t.Fatal("pruning silently substituted a record")
	}
}

func TestHistoryEmptyViewControls(t *testing.T) {
	v := historyView{page: 1}
	_, components := v.render()
	nav := components[0].(discord.ActionRowComponent).Components
	for i, c := range nav {
		if c.(discord.ButtonComponent).Disabled != (i != 3) {
			t.Fatal("only refresh should be enabled")
		}
	}
	selectMenu := components[1].(discord.ActionRowComponent).Components[0].(discord.StringSelectMenuComponent)
	if !selectMenu.Disabled {
		t.Fatal("empty selection enabled")
	}
	for _, c := range components[2].(discord.ActionRowComponent).Components {
		if !c.(discord.ButtonComponent).Disabled {
			t.Fatal("empty add enabled")
		}
	}
}
