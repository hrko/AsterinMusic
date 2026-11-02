package discordbot

import (
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"rainmusic/internal/music"
	"rainmusic/internal/source"
	"strings"
	"time"
)

func (a *App) edit(token, text string, components []discord.LayoutComponent) *discord.Message {
	text = truncate(text, 1900)
	m, _ := a.client.Rest.UpdateInteractionResponse(a.config.ApplicationID, token, discord.MessageUpdate{Content: &text, Components: &components, AllowedMentions: &discord.AllowedMentions{}})
	return m
}

func (a *App) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil || *e.GuildID() != a.config.GuildID {
		return
	}
	if err := e.DeferCreateMessage(e.SlashCommandInteractionData().CommandName() == "queue" && e.SlashCommandInteractionData().String("action") != "clear"); err != nil {
		return
	}
	s := a.getSession(*e.GuildID())
	data := e.SlashCommandInteractionData()
	user := e.User()
	reply := func(text string) { a.edit(e.Token(), text, nil) }
	switch data.CommandName() {
	case "autoplay":
		a.autoplayCommand(e, s)
	case "mix":
		a.mixCommand(e, s)
	case "play", "playnext":
		front := data.CommandName() == "playnext"
		if _, err := s.authorize(user.ID); err != nil {
			reply(err.Error())
			return
		}
		epoch := s.version()
		url := data.String("url")
		if front {
			input, err := source.Parse(url)
			if err != nil || input.Playlist {
				reply("/playnextには単曲URLを指定してください。付属リストは追加後のボタンで取り込めます。")
				return
			}
		}
		who := music.Requester{ID: user.ID.String(), Name: user.Username}
		if url == "" {
			_, err := s.accept(user.ID, who, nil, "", "", e.Channel().ID(), epoch)
			if err != nil {
				reply(err.Error())
			} else {
				reply("キューを再開しました。")
			}
			return
		}
		result, err := a.source.Inspect(a.ctx, url)
		if front && (err != nil || len(result.Tracks) != 1) {
			reply(source.AuthMessage(err, "曲を取得できませんでした。キューは変更していません。"))
			return
		}
		if len(result.Tracks) == 0 {
			reply(source.AuthMessage(err, "曲を取得できませんでした。公開状態とURLを確認してください。"))
			return
		}
		playlist := ""
		if result.Input.Playlist {
			playlist = result.Input.URL
		}
		n, entryID, addErr := s.acceptAt(user.ID, who, result.Tracks, playlist, "", e.Channel().ID(), epoch, music.Insertion{Front: front})
		text := fmt.Sprintf("%d曲を追加しました（追加者：%s）。", n, user.Username)
		if front && n > 0 {
			text += "\n自分の待機曲の先頭に追加しました。他の追加者との交代順は維持されます。"
		}
		if result.Input.PlaylistNotice != "" {
			text += "\n" + result.Input.PlaylistNotice
		}
		if len(result.Tracks) == 1 {
			text += "\n" + truncate(result.Tracks[0].Title, 200)
		}
		text += fmt.Sprintf("\n取得時の除外：%d件 / 容量による未追加：%d件", result.Skipped, len(result.Tracks)-n)
		if err != nil || result.Partial || result.Skipped > 0 {
			text += " 一部の項目は取得できませんでした。"
		}
		if result.LimitReached {
			text += " プレイリストの読み込みは先頭100件までです。"
		}
		if addErr != nil {
			text += "\n" + addErr.Error()
		}
		attached, label := result.Input.AttachedPlaylistURL, "プレイリストも取り込む"
		if result.Input.AttachedMixURL != "" {
			attached = result.Input.AttachedMixURL
			label = "Mixから追加（最大20曲）"
		}
		if n > 0 && attached != "" {
			o := a.offers.add(*e.GuildID(), user.ID, attached, result.Tracks[0].URL, music.Insertion{Front: front, AfterID: entryID})
			text += "\nこのURLにはリストも付いています。下のボタンで追加できます（本人のみ・15分間）。"
			m := a.edit(e.Token(), text, []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton(label, "playlist:"+o.ID))})
			if m != nil {
				a.offers.bind(o.ID, m.ID)
			}
		} else {
			reply(text)
		}
	case "queue":
		if data.String("action") == "clear" {
			if _, err := s.authorize(user.ID); err != nil {
				reply(err.Error())
				return
			}
			s.mu.Lock()
			s.epoch++
			s.queue.Clear()
			s.prefetchLocked()
			saveErr := s.queue.PersistenceError()
			autoOn := s.auto.enabled
			s.mu.Unlock()
			if saveErr != nil {
				reply("メモリ上の待機キューは消去しましたが、保存に失敗しました。再起動前にログを確認してください。")
				return
			}
			text := "待機キューを消去しました。"
			if autoOn {
				text += " 自動再生はONのままです。"
			}
			reply(text)
			return
		}
		s.queueViewMu.Lock()
		a.editQueue(e.Token(), s, user.ID, max(1, data.Int("page")), "")
		s.queueViewMu.Unlock()
	case "nowplaying":
		s.mu.Lock()
		var current *music.Entry
		if s.current != nil {
			copy := *s.current
			current = &copy
		}
		automatic, started, position := s.currentAuto, s.started, s.position
		s.mu.Unlock()
		if current == nil {
			reply("再生中の曲はありません。")
			return
		}
		origin := current.Requester.Name
		if automatic {
			origin = "自動再生（有効化：" + origin + "）"
		}
		state := "再生中"
		if !started {
			state = "再生準備中"
		}
		reply(fmt.Sprintf("%s：%s\n%s\n追加元：%s / 経過 %s / 音量 %d%%", state, current.Track.Title, current.Track.URL, origin, position.Truncate(time.Second), s.volume.Percent()))
	case "volume":
		if p, ok := data.OptInt("percent"); ok {
			if _, err := s.authorize(user.ID); err != nil {
				reply(err.Error())
				return
			}
			if err := s.volume.Set(p); err != nil {
				reply(err.Error())
				return
			}
		}
		reply(fmt.Sprintf("音量：%d%%（初期値30%%、100%%で元音量）", s.volume.Percent()))
	case "stop", "skip":
		if _, err := s.authorize(user.ID); err != nil {
			reply(err.Error())
			return
		}
		if data.CommandName() == "stop" {
			s.stop()
			reply("停止して切断しました。待機キューは保持しています。")
		} else if err := s.skip(); err != nil {
			reply(err.Error())
		} else {
			reply("次の曲へ進みます。")
		}
	}
}

func (a *App) onComponent(e *events.ComponentInteractionCreate) {
	if strings.HasPrefix(e.Data.CustomID(), "queue:") {
		a.queueComponent(e)
		return
	}
	id, ok := strings.CutPrefix(e.Data.CustomID(), "playlist:")
	if !ok {
		return
	}
	fail := func(text string) {
		_ = e.CreateMessage(discord.MessageCreate{Content: text, Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}})
	}
	if e.GuildID() == nil || *e.GuildID() != a.config.GuildID {
		fail("このサーバーでは操作できません。")
		return
	}
	s := a.getSession(*e.GuildID())
	if _, err := s.authorize(e.User().ID); err != nil {
		fail(err.Error())
		return
	}
	o, err := a.offers.claim(id, *e.GuildID(), e.User().ID, e.Message.ID, time.Now())
	if err != nil {
		fail(err.Error())
		return
	}
	if err := e.DeferUpdateMessage(); err != nil {
		a.offers.finish(id, false)
		return
	}
	if input, parseErr := source.Parse(o.URL); parseErr == nil && input.AttachedMixURL != "" {
		a.mixComponent(e, s, o)
		return
	}
	a.edit(e.Token(), "プレイリストを取り込み中です…", []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("取り込み中", "playlist:"+id).WithDisabled(true))})
	epoch := s.version()
	result, inspectErr := a.source.Inspect(a.ctx, o.URL)
	tracks, removed := excludeFirst(result.Tracks, o.TrackURL)
	n := 0
	var addErr error
	if len(tracks) > 0 {
		n, _, addErr = s.acceptAt(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, tracks, o.URL, o.ID, e.Channel().ID(), epoch, o.Insertion)
	}
	used := n > 0 || (inspectErr == nil && len(result.Tracks) > 0 && len(tracks) == 0)
	a.offers.finish(id, used)
	text := fmt.Sprintf("プレイリストから%d曲を追加しました（最初に追加した曲を%d件除外）。", n, removed)
	text += fmt.Sprintf("\n取得時の除外：%d件 / 未追加：%d件", result.Skipped, len(tracks)-n)
	if result.LimitReached {
		text += "\n取得範囲は先頭100件までです。"
	}
	if inspectErr != nil || result.Partial {
		text += " 一部の項目は取得できませんでした。"
	}
	if addErr != nil {
		text += "\n" + addErr.Error()
	}
	if !used {
		text += "\n追加できませんでした。ボタンから再試行できます。"
	}
	a.edit(e.Token(), text, []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("プレイリストも取り込む", "playlist:"+id).WithDisabled(used))})
}
