package discordbot

import (
	"encoding/json"
	"errors"
	"github.com/disgoorg/disgo/discord"
	"strings"
	"testing"
)

func commandData(t *testing.T, raw string) discord.SlashCommandInteractionData {
	t.Helper()
	var d discord.SlashCommandInteractionData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestQueryVisibility(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		private bool
	}{
		{`{"name":"nowplaying"}`, true}, {`{"name":"queue"}`, true}, {`{"name":"queue","options":[{"name":"clear","type":1}]}`, true},
		{`{"name":"volume"}`, true}, {`{"name":"volume","options":[{"name":"percent","type":4,"value":30}]}`, false},
		{`{"name":"autoplay","options":[{"name":"status","type":1}]}`, true}, {`{"name":"autoplay","options":[{"name":"on","type":1}]}`, false}, {`{"name":"play"}`, false},
	} {
		if privateQuery(commandData(t, tc.raw)) != tc.private {
			t.Fatal(tc.raw)
		}
	}
}
func TestInputPreflight(t *testing.T) {
	s := &session{}
	for _, raw := range []string{`{"name":"play","options":[{"name":"url","type":3,"value":"file:///secret"}]}`, `{"name":"volume","options":[{"name":"percent","type":4,"value":101}]}`, `{"name":"mix","options":[{"name":"url","type":3,"value":"https://nico.ms/sm1"}]}`} {
		if s.commandPreflight(commandData(t, raw), "1") == "" {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestResultMeaning(t *testing.T) {
	for _, tc := range []struct {
		n, wanted int
		partial   bool
		err       error
		color     int
		title     string
	}{
		{20, 20, false, nil, colorInfo, "Mixから20曲を追加しました"}, {12, 20, false, nil, colorWarning, "Mixから20曲を追加しました"}, {0, 0, false, nil, colorWarning, "追加できる曲がありませんでした"}, {0, 1, true, nil, colorError, "追加できませんでした"}, {1, 1, false, errors.New("音声接続に失敗しました。追加した曲はキューに保持しています。"), colorWarning, "Mixから20曲を追加しました"},
	} {
		e := resultEmbed("Mixから20曲を追加しました", "body", tc.n, tc.wanted, tc.partial, tc.err)
		if e.Color != tc.color || e.Title != tc.title {
			t.Fatalf("%+v", e)
		}
		if strings.Contains(e.Description, "0件") || strings.Contains(e.Description, "候補の取得") {
			t.Fatal("internal counts leaked")
		}
	}
}
func TestMessageEditsClearStaleRepresentation(t *testing.T) {
	for _, embed := range []*discord.Embed{nil, {Title: "done"}} {
		m := messageUpdate("text", embed, nil)
		if m.Embeds == nil || m.Components == nil || len(*m.Components) != 0 || m.Content == nil || m.AllowedMentions == nil {
			t.Fatal("incomplete edit")
		}
		if embed != nil && *m.Content != "" {
			t.Fatal("stale text")
		}
		if embed == nil && len(*m.Embeds) != 0 {
			t.Fatal("stale embed")
		}
	}
}
func TestUnknownErrorsDoNotLeak(t *testing.T) {
	if strings.Contains(userError(errors.New("https://secret.example?token=password")), "password") {
		t.Fatal("raw error leaked")
	}
}
