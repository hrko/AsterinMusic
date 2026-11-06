package discordbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

func TestQueueEditorInteractionFlow(t *testing.T) {
	a, s, _ := queueEditorApp(t)
	tracks := make([]music.Track, 12)
	for i := range tracks {
		tracks[i] = music.Track{URL: "https://www.youtube.com/watch?v=TcLLpZBWsck", Title: fmt.Sprint(i)}
	}
	s.queue.Add(music.Requester{ID: "2", Name: "self"}, tracks, "", "")
	s.queue.Add(music.Requester{ID: "3", Name: "other"}, tracks[:1], "", "")
	updates := 0
	a.client.Rest = rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "PATCH" {
			t.Fatalf("unexpected notification: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["allowed_mentions"] == nil {
			t.Fatal("mention suppression absent")
		}
		updates++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"5","channel_id":"7"}`)), Request: r}, nil
	})})))
	var response discord.InteractionResponseType
	respond := func(k discord.InteractionResponseType, d discord.InteractionResponseData, _ ...rest.RequestOpt) error {
		response = k
		if k == discord.InteractionResponseTypeCreateMessage {
			if d.(discord.MessageCreate).Flags != discord.MessageFlagEphemeral {
				t.Fatal("public rejection")
			}
		}
		return nil
	}
	component := func(id, value string) {
		t.Helper()
		kind := 2
		if value != "" {
			kind = 3
		}
		raw := fmt.Sprintf(`{"id":"10","application_id":"8","guild_id":"1","type":3,"token":"test","user":{"id":"2"},"data":{"custom_id":%q,"component_type":%d,"values":[%q]},"message":{"id":"5"}}`, id, kind, value)
		var interaction discord.ComponentInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		a.onComponent(&events.ComponentInteractionCreate{ComponentInteraction: interaction, Respond: respond})
	}
	component("qedit-open:2", "")
	if len(a.queueEditors) != 1 || updates != 1 {
		t.Fatal("editor not opened")
	}
	var v *queueEditor
	for _, view := range a.queueEditors {
		v = view
	}
	component(v.componentID("select"), "10")
	if v.selected != "10" {
		t.Fatal("selection failed")
	}
	stale := v.componentID("down")
	component(stale, "")
	if v.page != 2 || v.selected != "10" || v.entries[10].ID != "10" {
		t.Fatal("page follow failed")
	}
	before := updates
	component(stale, "")
	if updates != before || response != discord.InteractionResponseTypeCreateMessage {
		t.Fatal("stale click applied")
	}
	component(v.componentID("move"), "")
	if response != discord.InteractionResponseTypeModal {
		t.Fatal("no move modal")
	}
	modal := func(action, value string) {
		t.Helper()
		raw := fmt.Sprintf(`{"id":"11","application_id":"8","guild_id":"1","type":5,"token":"test","user":{"id":"2"},"message":{"id":"5"},"data":{"custom_id":%q,"components":[{"type":18,"component":{"type":4,"custom_id":"position","value":%q}}]}}`, v.componentID(action), value)
		var interaction discord.ModalSubmitInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		a.onQueueEditorModal(&events.ModalSubmitInteractionCreate{ModalSubmitInteraction: interaction, Respond: respond})
	}
	modal("move_submit", "0")
	if response != discord.InteractionResponseTypeCreateMessage || v.page != 2 {
		t.Fatal("invalid input changed queue")
	}
	modal("move_submit", "1")
	if v.page != 1 || v.entries[0].ID != "10" {
		t.Fatal("move modal failed")
	}
	component(v.componentID("remove"), "")
	if v.selected != "" || len(v.entries) != 11 {
		t.Fatal("delete failed")
	}
	modal("page_submit", "2")
	if v.page != 2 {
		t.Fatal("page modal failed")
	}
	component(v.componentID("owners"), "")
	if !v.choosingOwner {
		t.Fatal("owner screen missing")
	}
	component(v.componentID("owner"), "3")
	if v.requester != "3" || v.choosingOwner {
		t.Fatal("owner selection failed")
	}
	component(v.componentID("back"), "")
	if len(a.queueEditors) != 0 {
		t.Fatal("view not released")
	}
}
