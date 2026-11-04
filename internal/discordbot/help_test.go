package discordbot

import (
	"asterinmusic/internal/helpui"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"strings"
	"testing"
	"time"
)

func TestHelpPagesNavigationAndNoSession(t *testing.T) {
	c, err := helpui.New(Commands(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{helpCatalog: c, helpEpoch: "test-epoch"}
	now := time.Now()
	expires := now.Add(helpTTL).Unix()
	for _, p := range c.Pages() {
		m := a.helpMessage(p.ID, "123", expires)
		if m.Flags != discord.MessageFlagEphemeral || len(m.Embeds) != 1 || len(m.Components) != 2 || m.Content != "" {
			t.Fatalf("bad page %s", p.ID)
		}
		selected := 0
		for _, row := range m.Components {
			menu := row.(discord.ActionRowComponent).Components[0].(discord.StringSelectMenuComponent)
			if len(menu.Options) > 25 || len(menu.CustomID) > 100 {
				t.Fatal("component limits")
			}
			if _, _, ok := helpAccess(menu.CustomID, a.helpEpoch, "123", now); !ok {
				t.Fatal("generated ID invalid")
			}
			for _, o := range menu.Options {
				if o.Default {
					selected++
					if o.Value != p.ID {
						t.Fatal("stale selection")
					}
				}
			}
		}
		if selected != 1 {
			t.Fatal("exactly one menu must indicate current page")
		}
	}
	m := a.helpMessage("private-invalid-input", "123", expires)
	if m.Content == "" || strings.Contains(m.Content, "private-invalid-input") || m.Embeds[0].Title != "基本の使い方" {
		t.Fatal("invalid page fallback")
	}
	if a.sessions != nil {
		t.Fatal("help created playback session")
	}
}

func TestHelpAccessExpiresAndRejectsOtherOwnersOrBoots(t *testing.T) {
	now := time.Unix(10000, 0)
	valid := fmt.Sprintf("help:boot:123:%d:guide", now.Add(helpTTL).Unix())
	for _, tc := range []struct {
		id, epoch, owner string
		at               time.Time
		want             bool
	}{
		{valid, "boot", "123", now, true},
		{valid, "boot", "456", now, false},
		{valid, "newboot", "123", now, false},
		{valid, "boot", "123", now.Add(helpTTL), false},
		{valid, "boot", "123", now.Add(-time.Second), false},
		{"help:boot:123:invalid:guide", "boot", "123", now, false},
		{"help:boot:123:10900:unknown", "boot", "123", now, false},
	} {
		_, _, ok := helpAccess(tc.id, tc.epoch, tc.owner, tc.at)
		if ok != tc.want {
			t.Fatalf("unexpected access: %+v", tc)
		}
	}
}
