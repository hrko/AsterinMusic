package discordbot

import (
	"asterinmusic/internal/helpui"
	"encoding/json"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"testing"
	"time"
)

func TestHelpCommandBypassesPlaybackAndRepliesPrivately(t *testing.T) {
	c, err := helpui.New(Commands(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: t.Context(), helpCatalog: c, helpEpoch: "boot"}
	a.config.GuildID = 2
	authorizationFixture(t, a)
	for _, page := range []string{"", "command:play", "guide:add", "invalid"} {
		raw := fmt.Sprintf(`{"id":"10","application_id":"1","guild_id":"2","type":2,"user":{"id":"3","username":"test"},"data":{"id":"4","name":"help","type":1,"options":[{"name":"page","type":3,"value":%q}]}}`, page)
		var interaction discord.ApplicationCommandInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		calls := 0
		a.onCommand(&events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: interaction, Respond: func(kind discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			calls++
			m, ok := data.(discord.MessageCreate)
			if !ok || kind != discord.InteractionResponseTypeCreateMessage || m.Flags != discord.MessageFlagEphemeral || len(m.Embeds) != 1 {
				t.Fatal("not a private help response")
			}
			return nil
		}})
		if calls != 1 || a.sessions != nil {
			t.Fatal("help must reply without accessing playback")
		}
	}
}

func TestHelpComponentUpdatesSameMessageAndRejectsOtherUser(t *testing.T) {
	c, _ := helpui.New(Commands(), nil)
	a := &App{ctx: t.Context(), helpCatalog: c, helpEpoch: "boot"}
	a.config.GuildID = 2
	authorizationFixture(t, a)
	for _, owner := range []string{"3", "99"} {
		id := fmt.Sprintf("help:boot:%s:%d:command", owner, time.Now().Add(helpTTL).Unix())
		raw := fmt.Sprintf(`{"id":"10","application_id":"1","guild_id":"2","type":3,"user":{"id":"3","username":"test"},"data":{"custom_id":%q,"component_type":3,"values":["command:mix"]},"message":{"id":"20"}}`, id)
		var interaction discord.ComponentInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		calls := 0
		a.onComponent(&events.ComponentInteractionCreate{ComponentInteraction: interaction, Respond: func(kind discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			calls++
			if owner == "3" {
				m, ok := data.(discord.MessageUpdate)
				if !ok || kind != discord.InteractionResponseTypeUpdateMessage || (*m.Embeds)[0].Title != "/mix" {
					t.Fatal("did not update existing help")
				}
			} else {
				m, ok := data.(discord.MessageCreate)
				if !ok || m.Flags != discord.MessageFlagEphemeral {
					t.Fatal("rejection must be private")
				}
			}
			return nil
		}})
		if calls != 1 || a.sessions != nil {
			t.Fatal("component accessed playback")
		}
	}
}

func TestHelpAutocompleteUsesSharedPagesWithoutPlayback(t *testing.T) {
	c, err := helpui.New(Commands(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: t.Context(), helpCatalog: c}
	a.config.GuildID = 2
	authorizationFixture(t, a)
	for _, query := range []string{"", "再生", "play", "missing-page-xyz"} {
		raw := fmt.Sprintf(`{"id":"10","application_id":"1","guild_id":"2","type":4,"user":{"id":"3","username":"test"},"data":{"id":"4","name":"help","type":1,"options":[{"name":"page","type":3,"value":%q,"focused":true}]}}`, query)
		var interaction discord.AutocompleteInteraction
		if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
			t.Fatal(err)
		}
		calls := 0
		a.onHelpAutocomplete(&events.AutocompleteInteractionCreate{AutocompleteInteraction: interaction, Respond: func(kind discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			calls++
			result, ok := data.(discord.AutocompleteResult)
			if !ok || kind != discord.InteractionResponseTypeAutocompleteResult || len(result.Choices) != len(c.Search(query)) {
				t.Fatal("autocomplete response differs from catalog")
			}
			for _, choice := range result.Choices {
				value := choice.(discord.AutocompleteChoiceString).Value
				if _, ok := c.Find(value); !ok {
					t.Fatal("unreachable autocomplete page")
				}
			}
			return nil
		}})
		if calls != 1 || a.sessions != nil {
			t.Fatal("autocomplete accessed playback")
		}
	}
}
