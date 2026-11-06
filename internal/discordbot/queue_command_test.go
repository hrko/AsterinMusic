package discordbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

func TestQueueSubcommands(t *testing.T) {
	for _, sub := range []string{"show", "edit", "edit_target", "edit_missing", "clear", "cancel"} {
		t.Run(sub, func(t *testing.T) {
			a, s, _ := queueEditorApp(t)
			s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "one"}, {URL: "two"}}, "", "")
			var update discord.MessageUpdate
			a.client.Rest = rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "PATCH" {
					t.Fatalf("unexpected request %s", r.Method)
				}
				if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"5","channel_id":"7"}`)), Request: r}, nil
			})})))
			command, options := sub, ""
			if strings.HasPrefix(sub, "edit_") {
				command = "edit"
				id := "2"
				if sub == "edit_missing" {
					id = "3"
				}
				options = fmt.Sprintf(`,"options":[{"name":"user","type":6,"value":%q}]`, id)
			}
			if sub == "cancel" {
				command = "clear"
			}
			raw := fmt.Sprintf(`{"id":"10","application_id":"8","guild_id":"1","type":2,"token":"test","user":{"id":"2"},"data":{"name":"queue","type":1,"options":[{"name":%q,"type":1%s}]}}`, command, options)
			var interaction discord.ApplicationCommandInteraction
			if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
				t.Fatal(err)
			}
			if !privateQuery(interaction.SlashCommandInteractionData()) {
				t.Fatal("public queue response")
			}
			a.queueCommand(&events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: interaction}, s)
			if sub == "edit_missing" {
				if len(a.queueEditors) != 0 || update.Content == nil || !strings.Contains(*update.Content, "指定した追加者") {
					t.Fatal("explicit target silently replaced")
				}
				return
			}
			if update.Embeds == nil || len(*update.Embeds) != 1 {
				t.Fatal("missing screen")
			}
			_, pending := s.queue.Snapshot()
			if len(pending) != 2 {
				t.Fatal("command changed queue before confirmation")
			}
			if sub != "show" {
				if len(a.queueEditors) != 1 {
					t.Fatal("no bound view")
				}
				for _, v := range a.queueEditors {
					if v.message != 5 || v.clearConfirmation != (command == "clear") {
						t.Fatal("view binding")
					}
					if command == "clear" {
						action := "clear_confirm"
						if sub == "cancel" {
							action = "clear_cancel"
						}
						raw := fmt.Sprintf(`{"id":"11","application_id":"8","guild_id":"1","type":3,"token":"test","user":{"id":"2"},"data":{"custom_id":%q,"component_type":2},"message":{"id":"5"}}`, v.componentID(action))
						var click discord.ComponentInteraction
						if err := json.Unmarshal([]byte(raw), &click); err != nil {
							t.Fatal(err)
						}
						e := &events.ComponentInteractionCreate{ComponentInteraction: click, Respond: func(discord.InteractionResponseType, discord.InteractionResponseData, ...rest.RequestOpt) error {
							return nil
						}}
						a.queueEditorComponent(e)
						_, pending = s.queue.Snapshot()
						want := 0
						if sub == "cancel" {
							want = 2
						}
						if len(pending) != want || len(a.queueEditors) != 0 {
							t.Fatal("confirmation/cancel result")
						}
						s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "new"}}, "", "")
						a.queueEditorComponent(e)
						_, pending = s.queue.Snapshot()
						if len(pending) != want+1 {
							t.Fatal("confirmation replayed")
						}
					}
				}
			}
		})
	}
}

func TestClearConfirmedQueue(t *testing.T) {
	for _, change := range []string{"none", "added", "started", "left", "expired", "epoch"} {
		t.Run(change, func(t *testing.T) {
			a, s, v := queueEditorApp(t)
			s.queue.Add(music.Requester{ID: "2"}, []music.Track{{URL: "one"}, {URL: "two"}}, "", "")
			current, _ := s.queue.Start()
			s.current = &current
			_, v.entries = s.queue.Snapshot()
			s.auto.enabled = true
			switch change {
			case "added":
				s.queue.Add(music.Requester{ID: "3"}, []music.Track{{URL: "three"}}, "", "")
			case "started":
				s.queue.Finish(current.ID)
				s.queue.Start()
			case "left":
				a.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 2})
			case "expired":
				v.expires = time.Now().Add(-time.Second)
			case "epoch":
				s.epoch++
			}
			_, before := s.queue.Snapshot()
			err := s.clearConfirmedQueue(v)
			_, after := s.queue.Snapshot()
			if change == "none" {
				if err != nil || len(after) != 0 {
					t.Fatalf("clear failed: %v", err)
				}
			} else if err == nil || len(after) != len(before) {
				t.Fatalf("unsafe clear: %v", err)
			}
			if !s.auto.enabled || s.current != &current {
				t.Fatal("playback/settings changed")
			}
		})
	}
}
