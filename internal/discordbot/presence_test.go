package discordbot

import (
	"context"
	"errors"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"testing"
	"time"
)

func TestActivityPrivacyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name               string
		enabled            bool
		configured, joined []snowflake.ID
		guild              snowflake.ID
		playing            bool
		want               string
	}{
		{"dedicated", true, []snowflake.ID{1}, []snowflake.ID{1}, 1, true, "song"},
		{"disabled", false, []snowflake.ID{1}, []snowflake.ID{1}, 1, true, ""},
		{"shared even if only one joined", true, []snowflake.ID{1, 2}, []snowflake.ID{1}, 1, true, ""},
		{"unexpected guild", true, []snowflake.ID{1}, []snowflake.ID{1, 2}, 1, true, ""},
		{"unknown membership", true, []snowflake.ID{1}, nil, 1, true, ""},
		{"unknown config", true, nil, []snowflake.ID{1}, 1, true, ""},
		{"wrong session", true, []snowflake.ID{1}, []snowflake.ID{1}, 2, true, ""},
		{"stopped or preparing", true, []snowflake.ID{1}, []snowflake.ID{1}, 1, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := activityTitle(tc.enabled, tc.configured, tc.joined, tc.guild, tc.playing, "song"); got != tc.want {
				t.Fatal(got)
			}
		})
	}
	got := activityTitle(true, []snowflake.ID{1}, []snowflake.ID{1}, 1, true, strings.Repeat("曲", 150))
	if len([]rune(got)) != 128 || !strings.HasSuffix(got, "…") {
		t.Fatal(got)
	}
}

func TestPresenceLatestStateRetryClearAndReconnect(t *testing.T) {
	p := presencePublisher{}
	now := time.Unix(100, 0)
	title := "A"
	fail := false
	var got []string
	send := func(_ context.Context, s string) error {
		got = append(got, s)
		if fail {
			return errors.New("offline")
		}
		return nil
	}
	update := func(seconds int, revision uint64) {
		t.Helper()
		_ = p.update(context.Background(), now.Add(time.Duration(seconds)*time.Second), revision, func() string { return title }, send)
	}
	update(0, 1)
	title = "B"
	update(1, 1)
	title = "C"
	update(5, 1)
	update(10, 1) // unchanged
	title = "D"
	fail = true
	update(11, 1)
	title = ""
	fail = false
	update(16, 1) // clear, never retry obsolete D
	update(21, 2) // reconnect explicitly clears unchanged state
	if strings.Join(got, "|") != "A|C|D||" {
		t.Fatal(got)
	}
}

func TestPresenceInitialEmptyIsSent(t *testing.T) {
	p := presencePublisher{}
	sent := false
	err := p.update(context.Background(), time.Now(), 0, func() string { return "" }, func(_ context.Context, s string) error {
		sent = true
		if s != "" {
			t.Fatal(s)
		}
		return nil
	})
	if err != nil || !sent {
		t.Fatal(err, sent)
	}
}
