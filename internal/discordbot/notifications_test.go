package discordbot

import (
	"errors"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"testing"
)

func TestFailureSeries(t *testing.T) {
	var n failureNotice
	var edits []snowflake.ID
	send := func(id snowflake.ID, e discord.Embed) (snowflake.ID, error) {
		edits = append(edits, id)
		if id == 0 {
			return snowflake.ID(100 + len(edits)), nil
		}
		return id, nil
	}
	n.publish(0, 1, discord.Embed{Title: "first"}, send)
	n.publish(0, 1, discord.Embed{Title: "second"}, send)
	n.publish(0, 1, discord.Embed{Title: "stopped"}, send)
	n.publish(1, 1, discord.Embed{Title: "after successful audio"}, send)
	n.publish(1, 2, discord.Embed{Title: "different channel"}, send)
	want := []snowflake.ID{0, 101, 101, 0, 0}
	for i, v := range want {
		if edits[i] != v {
			t.Fatal(edits)
		}
	}
}
func TestNotificationFailureRetainsIdentity(t *testing.T) {
	n := failureNotice{sequence: 1, channel: 2, message: 3}
	n.publish(1, 2, discord.Embed{}, func(id snowflake.ID, e discord.Embed) (snowflake.ID, error) { return 0, errors.New("temporary") })
	if n.message != 3 {
		t.Fatal("lost message identity")
	}
}
