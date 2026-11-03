package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"strings"
)

const (
	colorInfo    = 0x527DA8
	colorWarning = 0xD9A441
	colorError   = 0xCB5656
)

// Explicitly clear the other representation and stale components on edits.
func messageUpdate(text string, embed *discord.Embed, components []discord.LayoutComponent) discord.MessageUpdate {
	embeds := []discord.Embed{}
	if embed != nil {
		embeds = append(embeds, *embed)
		text = ""
	}
	if components == nil {
		components = []discord.LayoutComponent{}
	}
	return discord.MessageUpdate{Content: &text, Embeds: &embeds, Components: &components, AllowedMentions: &discord.AllowedMentions{}}
}
func (a *App) editEmbed(token string, embed discord.Embed, components []discord.LayoutComponent) *discord.Message {
	m, err := a.client.Rest.UpdateInteractionResponse(a.config.ApplicationID, token, messageUpdate("", &embed, components))
	logOperationFailure("interaction.update_embed", err)
	return m
}
func (a *App) editFailure(token, text string) {
	a.editEmbed(token, discord.Embed{Title: "操作を完了できませんでした", Description: text, Color: colorError}, nil)
}
func singleLink(t music.Track) string { return trackLink(t, 160) }
func resultEmbed(title, body string, n, wanted int, partial bool, err error) discord.Embed {
	e := discord.Embed{Title: title, Description: body, Color: colorInfo}
	notes := []string{}
	if partial {
		notes = append(notes, "一部の曲を取得できませんでした。")
	}
	if n < wanted && n > 0 {
		notes = append(notes, fmt.Sprintf("%d曲はキューに追加できませんでした。", wanted-n))
	}
	if err != nil {
		notes = append(notes, userError(err))
	}
	if n == 0 {
		e.Title = "追加できる曲がありませんでした"
		e.Color = colorWarning
		if partial || err != nil {
			e.Title = "追加できませんでした"
			e.Color = colorError
		}
	} else if len(notes) > 0 {
		e.Color = colorWarning
	}
	if len(notes) > 0 {
		e.Description += "\n\n" + strings.Join(notes, "\n")
	}
	return e
}
func listLink(raw string, names ...string) string {
	label := "プレイリストを開く"
	if len(names) > 0 && strings.TrimSpace(names[0]) != "" {
		label = escapeLinkLabel(truncate(strings.Join(strings.Fields(names[0]), " "), 160))
	}
	if in, err := source.Parse(raw); err == nil {
		return "**[" + label + "](" + in.URL + ")**"
	}
	return "プレイリスト"
}
func originBody(label string, track music.Track, user string) string {
	return label + "：" + singleLink(track) + "\n" + queueDetail(track, queueMention(user))
}
func userError(err error) string {
	if err == nil {
		return ""
	}
	// These are application-authored errors. Never expose arbitrary upstream text.
	text := err.Error()
	for _, allowed := range []string{"音声チャンネルへ参加してください。", "Botと同じ音声チャンネルで操作してください。", "再起動処理中です。", "取得中に停止・キュー消去が行われました。もう一度追加してください。", "音声チャンネルが変更されました。", "停止処理中です。少し待って再実行してください。", "キューが満杯です。", "キューは空です。", "再生中の曲はありません。", "音声接続に失敗しました。追加した曲はキューに保持しています。", "キューはメモリ上に保持していますが、保存に失敗しました。再起動前にログと保存先を確認してください。"} {
		if text == allowed {
			return text
		}
	}
	return source.AuthMessage(err, "処理を完了できませんでした。しばらく待って再実行してください。")
}
func privateQuery(d discord.SlashCommandInteractionData) bool {
	switch d.CommandName() {
	case "queue":
		return d.String("action") != "clear"
	case "nowplaying":
		return true
	case "volume":
		_, set := d.OptInt("percent")
		return !set
	case "autoplay":
		return d.SubCommandName == nil || *d.SubCommandName == "status"
	}
	return false
}
func (s *session) commandPreflight(d discord.SlashCommandInteractionData, userID string) string {
	// Pure input validation before acknowledging a public operation.
	switch d.CommandName() {
	case "play", "playnext":
		raw := d.String("url")
		if raw != "" {
			in, e := source.Parse(raw)
			if e != nil {
				return "対応する動画または公開プレイリストのURLを指定してください。"
			}
			if d.CommandName() == "playnext" && in.Playlist {
				return "/playnextには単曲URLを指定してください。付属リストは追加後のボタンで取り込めます。"
			}
		} else if d.CommandName() == "playnext" {
			return "単曲URLを指定してください。"
		}
	case "mix":
		if _, e := source.MixInput(d.String("url")); e != nil {
			return "YouTube / YouTube Musicの動画URLを指定してください。"
		}
		if n, set := d.OptInt("count"); set && (n < 1 || n > 50) {
			return "曲数は1〜50で指定してください。"
		}
	case "volume":
		if n, set := d.OptInt("percent"); set && (n < 0 || n > 100) {
			return "音量は0〜100で指定してください。"
		}
	}
	return ""
}
