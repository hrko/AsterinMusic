package helpui_test

import (
	"asterinmusic/internal/discordbot"
	"asterinmusic/internal/helpui"
	"github.com/disgoorg/disgo/discord"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRegisteredCommandCoverageAndDiscordLimits(t *testing.T) {
	commands := discordbot.Commands()
	c, err := helpui.New(commands, map[string]string{"play": "123"})
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if _, ok := c.Find("command:" + cmd.CommandName()); !ok {
			t.Fatalf("missing %s", cmd.CommandName())
		}
	}
	length := func(s string) int { return len(utf16.Encode([]rune(s))) }
	for _, p := range c.Pages() {
		e := p.Embed
		total := length(e.Title) + length(e.Description)
		if length(e.Title) > 256 || length(e.Description) > 4096 || len(e.Fields) > 25 {
			t.Fatalf("embed limit: %s", p.ID)
		}
		for _, f := range e.Fields {
			total += length(f.Name) + length(f.Value)
			if f.Value == "" || length(f.Name) > 256 || length(f.Value) > 1024 {
				t.Fatalf("field limit: %s/%s", p.ID, f.Name)
			}
		}
		if total > 6000 {
			t.Fatalf("total limit: %s", p.ID)
		}
		if strings.Contains(e.Description, "{{") {
			t.Fatalf("unresolved reference: %s", p.ID)
		}
	}
	home, _ := c.Find(helpui.Home)
	if !strings.Contains(home.Embed.Description, "</play:123>") || !strings.Contains(home.Embed.Description, "`/skip`") {
		t.Fatal("link or fallback missing")
	}
	for _, q := range []string{"play", "再生", " PLAY "} {
		if len(c.Search(q)) == 0 {
			t.Fatalf("no matches: %s", q)
		}
	}
	if len(c.Search("no-such-page-xyz")) != 0 {
		t.Fatal("unknown search should be empty")
	}
	if len(c.Search("")) != len(c.Pages()) {
		t.Fatal("empty search coverage")
	}
	if _, ok := c.Find("command:missing"); ok {
		t.Fatal("invalid ID accepted")
	}
}

func TestRegistrationChangesDriveReference(t *testing.T) {
	commands := discordbot.Commands()
	for i, cmd := range commands {
		if cmd.CommandName() == "mix" {
			c := cmd.(discord.SlashCommandCreate)
			max := 7
			c.Options = []discord.ApplicationCommandOption{discord.ApplicationCommandOptionInt{Name: "limit", Description: "new description", Required: true, MaxValue: &max}}
			commands[i] = c
		}
	}
	c, err := helpui.New(commands, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Find("command:mix")
	if !strings.Contains(p.Embed.Fields[0].Value, "limit:値") || strings.Contains(p.Embed.Fields[0].Value, "[limit") {
		t.Fatal("syntax did not follow registration")
	}
	if !strings.Contains(p.Embed.Fields[1].Value, "最大7") || !strings.Contains(p.Embed.Fields[1].Value, "new description") {
		t.Fatal("argument did not follow registration")
	}
	commands = append(commands, discord.SlashCommandCreate{Name: "unexplained"})
	if _, err := helpui.New(commands, nil); err == nil {
		t.Fatal("new commands must have editorial help")
	}
}
