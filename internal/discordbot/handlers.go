package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"strings"
	"time"
)

func (a *App) edit(token, text string, components []discord.LayoutComponent) *discord.Message {
	text = truncate(text, 1900)
	m, _ := a.client.Rest.UpdateInteractionResponse(a.config.ApplicationID, token, messageUpdate(text, nil, components))
	return m
}

func (a *App) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil || *e.GuildID() != a.config.GuildID {
		return
	}
	s := a.getSession(*e.GuildID())
	data := e.SlashCommandInteractionData()
	user := e.User()
	problem := s.commandPreflight(data, user.ID.String())
	if problem == "" && !privateQuery(data) {
		if _, err := s.authorize(user.ID); err != nil {
			problem = userError(err)
		}
	}
	if problem != "" {
		_ = e.CreateMessage(discord.MessageCreate{Content: problem, Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}})
		return
	}
	if err := e.DeferCreateMessage(privateQuery(data)); err != nil {
		return
	}
	reply := func(text string) { a.edit(e.Token(), text, nil) }
	switch data.CommandName() {
	case "autoplay":
		a.autoplayCommand(e, s)
	case "mix":
		a.mixCommand(e, s)
	case "play", "playnext":
		front := data.CommandName() == "playnext"
		if _, err := s.authorize(user.ID); err != nil {
			reply(userError(err))
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
				reply(userError(err))
			} else {
				reply("キューを再開しました。")
			}
			return
		}
		result, err := a.source.Inspect(a.ctx, url)
		if front && (err != nil || len(result.Tracks) != 1) {
			a.editFailure(e.Token(), source.AuthMessage(err, "曲を取得できませんでした。キューは変更していません。"))
			return
		}
		if len(result.Tracks) == 0 {
			a.editFailure(e.Token(), source.AuthMessage(err, "曲を取得できませんでした。公開状態とURLを確認してください。"))
			return
		}
		playlist := ""
		if result.Input.Playlist {
			playlist = result.Input.URL
		}
		n, entryID, addErr := s.acceptAt(user.ID, who, result.Tracks, playlist, "", e.Channel().ID(), epoch, music.Insertion{Front: front})
		title := fmt.Sprintf("プレイリストから%d曲を追加しました", n)
		body := listLink(playlist, result.PlaylistTitle) + "\n" + queueMention(user.ID.String())
		if !result.Input.Playlist {
			title = "キューに追加しました"
			body = singleLink(result.Tracks[0]) + "\n" + queueDetail(result.Tracks[0], queueMention(user.ID.String()))
		}
		if front {
			title = "自分の次の番に追加しました"
		}
		embed := resultEmbed(title, body, n, len(result.Tracks), err != nil || result.Partial || result.Skipped > 0, addErr)
		if !result.Input.Playlist {
			embed.Thumbnail = trackThumbnail(result.Tracks[0])
		}
		if result.Input.PlaylistNotice != "" {
			embed.Description += "\n\n" + result.Input.PlaylistNotice
		}
		if result.Input.Playlist && result.LimitReached {
			embed.Color = colorWarning
			embed.Description += "\n\n先頭100件までを読み込みました。"
		}
		attached, label := result.Input.AttachedPlaylistURL, "プレイリストも取り込む"
		if result.Input.AttachedMixURL != "" {
			attached = result.Input.AttachedMixURL
			label = "Mixから追加（最大20曲）"
		}
		if n > 0 && attached != "" {
			o := a.offers.add(*e.GuildID(), user.ID, attached, result.Tracks[0].URL, music.Insertion{Front: front, AfterID: entryID})
			a.offers.rememberTrack(o.ID, result.Tracks[0])
			notice := "付属のプレイリストも追加できます。"
			if result.Input.AttachedMixURL != "" {
				notice = "付属のMixから最大20曲を追加できます。YouTube上の曲目・曲順とは異なる場合があります。"
			}
			embed.Description += "\n\n" + notice
			embed.Footer = &discord.EmbedFooter{Text: "本人のみ操作可能 · 15分間有効"}
			m := a.editEmbed(e.Token(), embed, []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton(label, "playlist:"+o.ID))})
			if m != nil {
				a.offers.bind(o.ID, m.ID)
			}
		} else {
			a.editEmbed(e.Token(), embed, nil)
		}
	case "queue":
		if data.String("action") == "clear" {
			if _, err := s.authorize(user.ID); err != nil {
				reply(userError(err))
				return
			}
			s.mu.Lock()
			if err := s.mutableLocked(); err != nil {
				s.mu.Unlock()
				reply(userError(err))
				return
			}
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
		origin := queueMention(current.Requester.ID)
		if automatic {
			origin = "自動再生"
		}
		state := "再生中"
		if !started {
			state = "再生準備中"
		}
		a.editEmbed(e.Token(), discord.Embed{Title: state, Description: singleLink(current.Track) + "\n" + queueDetail(current.Track, origin), Color: colorInfo, Thumbnail: trackThumbnail(current.Track), Footer: &discord.EmbedFooter{Text: fmt.Sprintf("表示時点 %s / %s · 音量 %d%%", queueDuration(int64(position/time.Second)), queueTrackDuration(current.Track), s.volume.Percent())}}, nil)
	case "volume":
		if p, ok := data.OptInt("percent"); ok {
			if _, err := s.authorize(user.ID); err != nil {
				reply(userError(err))
				return
			}
			if err := s.setVolume(p); err != nil {
				reply(userError(err))
				return
			}
		}
		label := fmt.Sprintf("音量：%d%%", s.volume.Percent())
		if _, set := data.OptInt("percent"); set {
			label = fmt.Sprintf("音量を%d%%に変更しました。", s.volume.Percent())
		}
		if s.volume.Percent() == 0 {
			label += "（ミュート）"
		}
		reply(label)
	case "stop", "skip":
		if _, err := s.authorize(user.ID); err != nil {
			reply(userError(err))
			return
		}
		if data.CommandName() == "stop" {
			if err := s.stop(); err != nil {
				reply(userError(err))
				return
			}
			reply("停止して切断しました。待機キューは保持しています。")
		} else if err := s.skip(); err != nil {
			reply(userError(err))
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
	tracks, _ := excludeFirst(result.Tracks, o.TrackURL)
	n := 0
	var addErr error
	if len(tracks) > 0 {
		n, _, addErr = s.acceptAt(e.User().ID, music.Requester{ID: e.User().ID.String(), Name: e.User().Username}, tracks, o.URL, o.ID, e.Channel().ID(), epoch, o.Insertion)
	}
	used := n > 0 || (inspectErr == nil && len(result.Tracks) > 0 && len(tracks) == 0)
	a.offers.finish(id, used)
	embed := resultEmbed(fmt.Sprintf("プレイリストから%d曲を追加しました", n), listLink(o.URL, result.PlaylistTitle)+"\n\n"+originBody("最初に追加した曲", o.Track, e.User().ID.String()), n, len(tracks), inspectErr != nil || result.Partial, addErr)
	embed.Thumbnail = trackThumbnail(o.Track)
	if result.LimitReached {
		embed.Description += "\n\n先頭100件までを読み込みました。"
		if n > 0 {
			embed.Color = colorWarning
		}
	}
	var components []discord.LayoutComponent
	if !used {
		embed.Description += "\n\n期限内なら再試行できます。"
		components = []discord.LayoutComponent{discord.NewActionRow(discord.NewPrimaryButton("再試行", "playlist:"+id))}
	}
	a.editEmbed(e.Token(), embed, components)
}
