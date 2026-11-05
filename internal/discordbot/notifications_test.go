package discordbot

import (
	"asterinmusic/internal/logging"
	"bytes"
	"errors"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNotificationAccessFailureLogsDestination(t *testing.T) {
	var out bytes.Buffer
	logger, _ := logging.New(&out, logging.Options{Format: "json"})
	transport := historyTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":50001,"message":"PRIVATE_DETAIL"}`)), Request: r}, nil
	})
	a := &App{ctx: t.Context(), client: &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
	s := &session{app: a, text: 7, logger: logger}
	s.notifyFailure(7, nil, "PRIVATE_NOTICE", true)
	for _, want := range []string{`"notification_channel_id":"7"`, `"notification_operation":"create"`, `"http_status":403`, `"discord_code":50001`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("notification details leaked")
	}
}

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
