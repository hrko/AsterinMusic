package discordbot

import (
	"github.com/disgoorg/disgo/discord"
	"rainmusic/internal/music"
	"testing"
	"time"
)

func TestPlaynextRequiresURL(t *testing.T) {
	for _, cmd := range Commands() {
		if cmd.CommandName() == "playnext" {
			c := cmd.(discord.SlashCommandCreate)
			if len(c.Options) != 1 {
				t.Fatal("unexpected options")
			}
			option := c.Options[0].(discord.ApplicationCommandOptionString)
			if option.Name != "url" || !option.Required {
				t.Fatal("URL must be required")
			}
			return
		}
	}
	t.Fatal("playnext missing")
}
func TestOfferRetainsEntryAnchorAndInsertion(t *testing.T) {
	o := newOffers()
	created := o.add(1, 2, "list", "video", music.Insertion{Front: true, AfterID: "42"})
	o.bind(created.ID, 3)
	claimed, err := o.claim(created.ID, 1, 2, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.Insertion.Front || claimed.Insertion.AfterID != "42" {
		t.Fatal("placement lost")
	}
	if _, err = o.claim(created.ID, 1, 2, 3, time.Now()); err == nil {
		t.Fatal("double claim")
	}
	o.finish(created.ID, false)
	retry, err := o.claim(created.ID, 1, 2, 3, time.Now())
	if err != nil || retry.Insertion != claimed.Insertion {
		t.Fatal("retry placement changed")
	}
}
