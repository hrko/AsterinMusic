package discordbot

import "github.com/disgoorg/disgo/discord"

func Commands() []discord.ApplicationCommandCreate {
	zero, hundred, one, fifty := 0, 100, 1, 50
	return []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{Name: "playnext", Description: "自分の次の番に再生する曲を追加", Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionString{Name: "url", Description: "単曲URL（付属リストはボタンで追加）", Required: true}}},
		discord.SlashCommandCreate{Name: "autoplay", Description: "手動キューが空のとき関連曲を自動再生", Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionSubCommand{Name: "on", Description: "自動再生を有効化"}, discord.ApplicationCommandOptionSubCommand{Name: "off", Description: "自動補充を停止（現在の曲は継続）"}, discord.ApplicationCommandOptionSubCommand{Name: "status", Description: "自動再生の状態と起点を表示"}}},
		discord.SlashCommandCreate{Name: "mix", Description: "動画を起点にMixから有限件を追加（自動補充なし）", Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionString{Name: "url", Description: "YouTube / YouTube Musicの動画URL", Required: true}, discord.ApplicationCommandOptionInt{Name: "count", Description: "起点の曲を含む追加上限（既定20曲）", MinValue: &one, MaxValue: &fifty}}},
		discord.SlashCommandCreate{Name: "play", Description: "曲・プレイリストを追加、またはキューを再開", Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionString{Name: "url", Description: "YouTube / YouTube Music / ニコニコ動画のURL"}}},
		discord.SlashCommandCreate{Name: "stop", Description: "再生を終了して切断（待機キューは保持）"},
		discord.SlashCommandCreate{Name: "skip", Description: "次の曲へ進む"},
		discord.SlashCommandCreate{Name: "queue", Description: "公平な再生予定順を表示・消去", Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{Name: "action", Description: "表示または待機キューを全消去", Choices: []discord.ApplicationCommandOptionChoiceString{{Name: "表示", Value: "show"}, {Name: "全消去", Value: "clear"}}},
			discord.ApplicationCommandOptionInt{Name: "page", Description: "表示ページ", MinValue: &one},
		}},
		discord.SlashCommandCreate{Name: "nowplaying", Description: "再生中の曲・追加者・音量を表示"},
		discord.SlashCommandCreate{Name: "volume", Description: "音量を表示・変更（初期30%）", Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionInt{Name: "percent", Description: "0でミュート、100で元音量", MinValue: &zero, MaxValue: &hundred}}},
	}
}
