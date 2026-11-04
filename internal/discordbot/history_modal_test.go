package discordbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asterinmusic/internal/config"
	"asterinmusic/internal/history"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

func TestHistoryModalRetryUpdatesOriginalView(t *testing.T) {
	ctx := context.Background()
	store, err := history.Open(filepath.Join(t.TempDir(), "history.sqlite"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := range 11 {
		if err := store.Append(ctx, history.Record{OccurrenceID: fmt.Sprint(i), GuildID: "2", VoiceID: "3", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
	voice := snowflake.ID(3)
	c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
	var paths, bodies []string
	transport := historyTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b))
		id := "9"
		if strings.Contains(r.URL.Path, "original-token") {
			id = "4"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":"%s","channel_id":"7"}`, id))), Request: r}, nil
	})
	a := &App{ctx: ctx, config: config.Config{GuildID: 2, ApplicationID: 8}, historyStore: store, client: &bot.Client{Caches: c, Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
	v := &historyView{id: "view", owner: 1, guild: 2, voice: 3, message: 4, originalToken: "original-token", expires: time.Now().Add(time.Minute)}
	snap, _ := store.Snapshot(ctx, "2", "3")
	if err := v.load(ctx, store, snap, 1); err != nil {
		t.Fatal(err)
	}
	a.rememberHistory(v)
	submit := func(action, value string, message int) {
		t.Helper()
		var interaction discord.ModalSubmitInteraction
		raw := fmt.Sprintf(`{"id":"5","application_id":"8","type":5,"token":"modal-token","guild_id":"2","channel":{"id":"7","type":0},"member":{"user":{"id":"1"}},"message":{"id":"%d"},"data":{"custom_id":"%s","components":[{"type":18,"component":{"type":4,"custom_id":"page","value":%q}}]}}`, message, v.componentID(action), value)
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		a.onHistoryModal(&events.ModalSubmitInteractionCreate{ModalSubmitInteraction: interaction, Respond: func(_ discord.InteractionResponseType, _ discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			return nil
		}})
	}
	submit("submit", "abc", 4)
	if v.page != 1 || v.retryMessage != 9 || !strings.Contains(bodies[0], "1〜2の整数") || !strings.Contains(bodies[0], "入力し直す") {
		t.Fatalf("invalid result: %v", bodies)
	}
	got, action, err := a.historyAction(v.componentID("retry"), 1, &v.guild, 9)
	if err != nil || action != "retry" {
		t.Fatal(err)
	}
	got.mu.Unlock()
	if _, _, err := a.historyAction(v.componentID("retry"), 1, &v.guild, 10); err == nil {
		t.Fatal("unbound retry accepted")
	}
	modal, _ := json.Marshal(v.pageModal("resubmit"))
	if !strings.Contains(string(modal), "ページ番号（1〜2）") {
		t.Fatal(string(modal))
	}
	submit("resubmit", "0", 9)
	if v.page != 1 {
		t.Fatal("invalid retry moved page")
	}
	submit("resubmit", "2", 9)
	if v.page != 2 || v.message != 4 || v.retryMessage != 0 {
		t.Fatalf("wrong view: page=%d message=%s retry=%s", v.page, v.message, v.retryMessage)
	}
	if !strings.Contains(paths[len(paths)-2], "original-token") || !strings.Contains(bodies[len(bodies)-1], "指定したページを表示しました") {
		t.Fatal(paths, bodies)
	}
}
