package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

const mixNotice = "Mixの候補を取得して追加しました。YouTubeで表示される曲目・曲順とは異なる場合があります。自動補充は行いません。"

func mixReport(n int, r source.Result, selection source.MixSelection, fetchErr, addErr error) string {
	text := fmt.Sprintf("Mixから%d曲を追加しました。\n重複除外：%d件 / 起点の重複除外：%d件 / 対象外：%d件\n指定件数による未追加：%d件 / キューへの未追加：%d件", n, selection.Duplicates, selection.SeedExcluded, r.Skipped, selection.OverLimit, len(selection.Tracks)-n)
	if r.LimitReached {
		text += "\n候補の取得は先頭100件までです。"
	}
	if fetchErr != nil || r.Partial {
		text += "\n一部の候補を取得できませんでした。"
	}
	if addErr != nil {
		text += "\n" + addErr.Error()
	}
	if n > 0 {
		text += "\n" + mixNotice
	} else {
		text += "\n追加できませんでした。取得不能件数は検出できた範囲です。"
	}
	return text
}

func (a *App) mixCommand(e *events.ApplicationCommandInteractionCreate, s *session) {
	reply := func(t string) { a.edit(e.Token(), t, nil) }
	if _, err := s.authorize(e.User().ID); err != nil {
		reply(err.Error())
		return
	}
	count, ok := e.SlashCommandInteractionData().OptInt("count")
	if !ok {
		count = source.DefaultMixCount
	}
	if count < 1 || count > source.MaxMixCount {
		reply("countは1〜50で指定してください。")
		return
	}
	raw := e.SlashCommandInteractionData().String("url")
	if _, err := source.MixInput(raw); err != nil {
		reply(err.Error())
		return
	}
	epoch := s.version()
	seed, err := a.source.Inspect(a.ctx, raw)
	if err != nil || len(seed.Tracks) != 1 {
		reply("起点の曲を取得できませんでした。キューは変更していません。")
		return
	}
	result, fetchErr := a.source.InspectMix(a.ctx, raw)
	if len(result.Tracks) == 0 {
		reply("Mixを取得できませんでした。単曲への切り替えは行わず、キューは変更していません。")
		return
	}
	selection := source.SelectMix(result.Tracks, seed.Tracks[0], count, true)
	n, addErr := s.accept(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, selection.Tracks, result.Input.URL, "", e.Channel().ID(), epoch)
	reply(mixReport(n, result, selection, fetchErr, addErr))
}

// The common component handler has already claimed the offer and acknowledged it.
func (a *App) mixComponent(e *events.ComponentInteractionCreate, s *session, o offer) {
	epoch := s.version()
	a.edit(e.Token(), "Mixの候補を取得中です…", []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("取得中", "playlist:"+o.ID).WithDisabled(true))})
	result, fetchErr := a.source.InspectMix(a.ctx, o.URL)
	selection := source.SelectMix(result.Tracks, music.Track{URL: o.TrackURL}, source.DefaultMixCount, false)
	n := 0
	var addErr error
	if len(selection.Tracks) > 0 {
		n, _, addErr = s.acceptAt(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, selection.Tracks, o.URL, o.ID, e.Channel().ID(), epoch, o.Insertion)
	}
	used := n > 0
	a.offers.finish(o.ID, used)
	text := mixReport(n, result, selection, fetchErr, addErr)
	if !used {
		text += "\n期限内ならボタンから再試行できます。"
	}
	label := "Mixから追加（最大20曲）"
	if used {
		label = "取り込み済み"
	}
	a.edit(e.Token(), text, []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton(label, "playlist:"+o.ID).WithDisabled(used))})
}
