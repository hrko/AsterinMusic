package discordbot

import (
	"asterinmusic/internal/logging"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponseEditFailureRetainsOperationWithoutToken(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json"})
	transport := historyTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":50013,"message":"PRIVATE_RESPONSE"}`)), Request: r}, nil
	})
	a := &App{ctx: t.Context(), logger: l, client: &bot.Client{Rest: rest.New(rest.NewClient("PRIVATE_BOT", rest.WithHTTPClient(&http.Client{Transport: transport})))}}
	a.config.ApplicationID = 1
	a.config.GuildID = 2
	ref := responseRef{token: "PRIVATE_INTERACTION", operation: 42}
	a.edit(ref, "PRIVATE_TEXT", nil)
	a.editEmbed(ref, discord.Embed{Description: "PRIVATE_TEXT"}, nil)
	a.historyReply(ref, "PRIVATE_TEXT")
	text := out.String()
	if strings.Contains(text, "PRIVATE") || strings.Count(text, `"operation_id":"42"`) != 3 || strings.Count(text, `"http_status":403`) != 3 {
		t.Fatalf("unsafe or uncorrelated response failure: %s", text)
	}
}
