package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

func (a *App) mixCommand(e *events.ApplicationCommandInteractionCreate, s *session) {
	reply := func(t string) { a.edit(responseFor(e), t, nil) }
	if _, err := s.authorize(e.User().ID); err != nil {
		reply(userError(err))
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
		reply(userError(err))
		return
	}
	epoch := s.version()
	seed, err := a.source.Inspect(a.ctx, raw)
	if err != nil || len(seed.Tracks) != 1 {
		a.editFailure(responseFor(e), "起点の曲を取得できませんでした。キューは変更していません。")
		return
	}
	result, fetchErr := a.source.InspectMix(a.ctx, raw)
	if len(result.Tracks) == 0 {
		a.editFailure(responseFor(e), "Mixを取得できませんでした。キューは変更していません。")
		return
	}
	selection := source.SelectMix(result.Tracks, seed.Tracks[0], count, true)
	n, addErr := s.accept(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, selection.Tracks, result.Input.URL, "", e.Channel().ID(), epoch)
	if a.rejectDeferredAuthorization(responseFor(e), addErr, false) {
		return
	}
	embed := resultEmbed(fmt.Sprintf("Mixから%d曲を追加しました", n), originBody("起点", seed.Tracks[0], e.User().ID.String()), n, len(selection.Tracks), fetchErr != nil || result.Partial, addErr)
	embed.Thumbnail = trackThumbnail(seed.Tracks[0])
	a.editEmbed(responseFor(e), embed, nil)
}

// The common component handler has already claimed the offer and acknowledged it.
func (a *App) mixComponent(e *events.ComponentInteractionCreate, s *session, o offer) {
	epoch := s.version()
	a.edit(responseFor(e), "Mixの候補を取得中です…", []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("取得中", "playlist:"+o.ID).WithDisabled(true))})
	result, fetchErr := a.source.InspectMix(a.ctx, o.URL)
	selection := source.SelectMix(result.Tracks, music.Track{URL: o.TrackURL}, source.DefaultMixCount, false)
	n := 0
	var addErr error
	if len(selection.Tracks) > 0 {
		n, _, addErr = s.acceptAt(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, selection.Tracks, o.URL, o.ID, e.Channel().ID(), epoch, o.Insertion)
	}
	if isAuthorizationError(addErr) {
		a.offers.finish(o.ID, false)
		a.rejectDeferredAuthorization(responseFor(e), addErr, true)
		return
	}
	used := n > 0 || (fetchErr == nil && len(result.Tracks) > 0 && len(selection.Tracks) == 0)
	a.offers.finish(o.ID, used)
	embed := resultEmbed(fmt.Sprintf("Mixから%d曲を追加しました", n), originBody("起点", o.Track, e.User().ID.String()), n, len(selection.Tracks), fetchErr != nil || result.Partial, addErr)
	embed.Thumbnail = trackThumbnail(o.Track)
	var components []discord.LayoutComponent
	if !used {
		embed.Description += "\n\n期限内なら再試行できます。"
		components = []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("再試行", "playlist:"+o.ID))}
	}
	a.editEmbed(responseFor(e), embed, components)
}
