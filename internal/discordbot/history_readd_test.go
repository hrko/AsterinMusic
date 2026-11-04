package discordbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asterinmusic/internal/config"
	"asterinmusic/internal/history"
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type historyTransport func(*http.Request) (*http.Response, error)

func (f historyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHistoryReaddUsesFreshSingleTrackAndCurrentActor(t *testing.T) {
	for _, scenario := range []string{"append", "front", "moved", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, err := history.Open(filepath.Join(t.TempDir(), "history.sqlite"), 100)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			old := autoTrack("abcdefghijk")
			old.URL += "&list=PL123&si=tracking"
			if err := store.Append(ctx, history.Record{OccurrenceID: "original", GuildID: "2", VoiceID: "3", StartedAt: time.Now(), Automatic: true, Entry: music.Entry{Track: old, Requester: music.Requester{ID: "99"}}}); err != nil {
				t.Fatal(err)
			}
			c := cache.New(cache.WithCaches(cache.FlagVoiceStates))
			voice := snowflake.ID(3)
			c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &voice})
			var replies []string
			transport := historyTransport(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				replies = append(replies, string(body))
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"4","channel_id":"7"}`)), Request: r}, nil
			})
			a := &App{ctx: ctx, config: config.Config{GuildID: 2, ApplicationID: 8}, historyStore: store, client: &bot.Client{Caches: c, Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}, sessions: make(map[snowflake.ID]*session)}
			s := a.getSession(2)
			s.channel = 3
			s.running = true
			s.queue.Add(music.Requester{ID: "1"}, []music.Track{autoTrack("xxxxxxxxxxx")}, "", "")
			a.source = source.New("test-yt-dlp")
			a.source.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
				if strings.Contains(strings.Join(args, " "), "list=") || strings.Contains(strings.Join(args, " "), "si=") {
					t.Fatal("attached list/tracking passed to source")
				}
				if scenario == "moved" {
					next := snowflake.ID(9)
					c.AddVoiceState(discord.VoiceState{GuildID: 2, UserID: 1, ChannelID: &next})
				}
				if scenario == "stopped" {
					s.mu.Lock()
					s.epoch++
					s.mu.Unlock()
				}
				_, err := io.WriteString(out, `{"id":"abcdefghijk","title":"fresh title","duration":123}`)
				return err
			}
			snap, err := store.Snapshot(ctx, "2", "3")
			if err != nil {
				t.Fatal(err)
			}
			v := &historyView{owner: 1, guild: 2, voice: 3, message: 4, expires: time.Now().Add(time.Minute)}
			if err := v.load(ctx, store, snap, 1); err != nil {
				t.Fatal(err)
			}
			v.selected = v.rows[0].ID
			var interaction discord.ComponentInteraction
			if err := json.Unmarshal([]byte(`{"id":"5","application_id":"8","type":3,"token":"test","guild_id":"2","channel":{"id":"7","type":0},"member":{"user":{"id":"1","username":"actor"}},"message":{"id":"4"},"data":{"component_type":2,"custom_id":"unused"}}`), &interaction); err != nil {
				t.Fatal(err)
			}
			a.readdHistory(&events.ComponentInteractionCreate{ComponentInteraction: interaction}, v, scenario == "front")
			_, pending := s.queue.Snapshot()
			if scenario == "moved" || scenario == "stopped" {
				if len(pending) != 1 {
					t.Fatal("stale operation added track")
				}
				return
			}
			if len(pending) != 2 {
				t.Fatalf("pending=%d replies=%v", len(pending), replies)
			}
			index := 1
			if scenario == "front" {
				index = 0
			}
			added := pending[index]
			if added.Track.Title != "fresh title" || added.Requester.ID != "1" || added.PlaylistURL != "" || v.selected != 0 {
				t.Fatalf("wrong addition: %+v", added)
			}
			r, err := store.Get(ctx, "2", "3", v.rows[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Entry.Track.Title != old.Title || !r.Automatic {
				t.Fatal("past metadata rewritten")
			}
			if len(replies) != 2 {
				t.Fatal("missing view update or result")
			}
		})
	}
}
