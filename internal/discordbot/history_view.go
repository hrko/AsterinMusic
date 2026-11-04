package discordbot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"asterinmusic/internal/history"
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

const historyViewTTL = 15 * time.Minute
const historyViewLimit = 1024

var errHistoryView = errors.New("この履歴画面は利用できません。/historyを実行し直してください。")

type historyView struct {
	mu                           sync.Mutex
	id                           string
	owner, guild, voice, message snowflake.ID
	expires                      time.Time
	snapshot                     history.Snapshot
	page                         int
	rows                         []history.Record
	selected                     int64
	revision                     uint64
	originalToken                string
	retryMessage                 snowflake.ID
}

func (a *App) historyVoice(guild, user snowflake.ID) (snowflake.ID, error) {
	state, ok := a.client.Caches.VoiceState(guild, user)
	if !ok || state.ChannelID == nil {
		return 0, errors.New("履歴を確認するVCに参加してください。")
	}
	return *state.ChannelID, nil
}

func (a *App) rememberHistory(v *historyView) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.historyViews == nil {
		a.historyViews = make(map[string]*historyView)
	}
	for id, old := range a.historyViews {
		if time.Now().After(old.expires) {
			delete(a.historyViews, id)
		}
	}
	if len(a.historyViews) >= historyViewLimit {
		return false
	}
	a.historyViews[v.id] = v
	return true
}

func (v *historyView) valid(owner, guild, voice, message snowflake.ID, revision uint64, now time.Time) bool {
	return owner == v.owner && guild == v.guild && voice == v.voice && message == v.message && revision == v.revision && now.Before(v.expires)
}

// Returns a locked view; the caller must unlock. TryLock prevents long source
// fetches from making subsequent Discord interactions miss their ack deadline.
func (a *App) historyAction(custom string, user snowflake.ID, guild *snowflake.ID, message snowflake.ID) (*historyView, string, error) {
	parts := strings.Split(custom, ":")
	if len(parts) != 4 || parts[0] != "history" || guild == nil || *guild != a.config.GuildID {
		return nil, "", errHistoryView
	}
	rev, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return nil, "", errHistoryView
	}
	a.mu.Lock()
	v := a.historyViews[parts[1]]
	a.mu.Unlock()
	if v == nil {
		return nil, "", errHistoryView
	}
	if !v.mu.TryLock() {
		return nil, "", errors.New("処理中です。少し待って操作してください。")
	}
	voice, err := a.historyVoice(*guild, user)
	if parts[3] == "retry" || parts[3] == "resubmit" {
		if message == 0 || message != v.retryMessage {
			v.mu.Unlock()
			return nil, "", errHistoryView
		}
		message = v.message
	}
	if err != nil || !v.valid(user, *guild, voice, message, rev, time.Now()) {
		v.mu.Unlock()
		return nil, "", errHistoryView
	}
	return v, parts[3], nil
}

func (v *historyView) componentID(action string) string {
	return fmt.Sprintf("history:%s:%d:%s", v.id, v.revision, action)
}

func (v *historyView) load(ctx context.Context, store *history.Store, snap history.Snapshot, page int) error {
	rows, err := store.Page(ctx, snap, page)
	if err != nil {
		return err
	}
	v.snapshot = snap
	v.page = page
	v.rows = rows
	v.selected = 0
	return nil
}

func (v *historyView) selectRecord(id int64) bool {
	for _, r := range v.rows {
		if r.ID == id {
			v.selected = id
			return true
		}
	}
	return false
}

func (v *historyView) render() (discord.Embed, []discord.LayoutComponent) {
	pages := max(int64(1), (v.snapshot.Count+history.PageSize-1)/history.PageSize)
	body := fmt.Sprintf("<#%s>\n\n", v.voice)
	indent := strings.Repeat(" ", len(strconv.Itoa((v.page-1)*history.PageSize+1))+2)
	options := make([]discord.StringSelectMenuOption, 0, len(v.rows))
	for i, r := range v.rows {
		n := (v.page-1)*history.PageSize + i + 1
		who := queueMention(r.Entry.Requester.ID)
		if r.Automatic {
			who = "自動再生"
		}
		t := compactQueueTrack(r.Entry.Track)
		body += fmt.Sprintf("%d. %s  \n%s-# <t:%d:R> · %s\n", n, queueLink(t), indent, r.StartedAt.Unix(), queueDetail(t, who))
		options = append(options, discord.NewStringSelectMenuOption(truncate(fmt.Sprintf("%d. %s", n, r.Entry.Track.Title), 90), strconv.FormatInt(r.ID, 10)).WithDefault(r.ID == v.selected))
	}
	if len(v.rows) == 0 {
		body += "このVCの再生履歴はまだありません。"
	}
	embed := discord.Embed{Title: "再生履歴", Description: body, Color: colorInfo, Footer: &discord.EmbedFooter{Text: fmt.Sprintf("%d件 · 新しい順 · 再生開始の記録 · 操作は15分間有効", v.snapshot.Count)}}
	components := []discord.LayoutComponent{discord.NewActionRow(
		discord.NewSecondaryButton("◀ 前へ", v.componentID("prev")).WithDisabled(v.page <= 1),
		discord.NewSecondaryButton(fmt.Sprintf("%d / %d", v.page, pages), v.componentID("jump")).WithDisabled(len(v.rows) == 0),
		discord.NewSecondaryButton("次へ ▶", v.componentID("next")).WithDisabled(int64(v.page) >= pages),
		discord.NewSecondaryButton("↻ 更新", v.componentID("refresh")),
	)}
	if len(options) == 0 {
		options = append(options, discord.NewStringSelectMenuOption("履歴なし", "0"))
	}
	components = append(components, discord.NewActionRow(discord.NewStringSelectMenu(v.componentID("select"), "再追加する曲を選択", options...).WithDisabled(len(v.rows) == 0)),
		discord.NewActionRow(discord.NewPrimaryButton("キューに追加", v.componentID("add")).WithDisabled(v.selected == 0),
			discord.NewSecondaryButton("自分のキューの先頭に追加", v.componentID("front")).WithDisabled(v.selected == 0)))
	return embed, components
}

func (a *App) showHistory(token string, v *historyView) bool {
	v.revision++
	v.retryMessage = 0
	embed, components := v.render()
	if m := a.editEmbed(token, embed, components); m != nil {
		v.message = m.ID
		return true
	}
	return false
}

func historyProblem(err error) string {
	if errors.Is(err, history.ErrExpired) {
		return "履歴が整理されたため、表示を維持できません。更新してください。"
	}
	if errors.Is(err, history.ErrPage) {
		return "表示範囲内のページ番号を入力してください。"
	}
	return "履歴を取得できませんでした。少し待って再実行してください。"
}

func (a *App) historyCommand(e *events.ApplicationCommandInteractionCreate) {
	voice, err := a.historyVoice(*e.GuildID(), e.User().ID)
	if err != nil {
		a.edit(e.Token(), err.Error(), nil)
		return
	}
	if a.historyStore == nil {
		a.edit(e.Token(), "履歴を利用できません。", nil)
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	snap, err := a.historyStore.Snapshot(ctx, e.GuildID().String(), voice.String())
	if err != nil {
		a.logOperationFailure("history.snapshot", err)
		a.edit(e.Token(), historyProblem(err), nil)
		return
	}
	v := &historyView{id: rand.Text(), originalToken: e.Token(), owner: e.User().ID, guild: *e.GuildID(), voice: voice, expires: time.Now().Add(historyViewTTL)}
	if err = v.load(ctx, a.historyStore, snap, 1); err != nil {
		a.edit(e.Token(), historyProblem(err), nil)
		return
	}
	current, err := a.historyVoice(v.guild, v.owner)
	if err != nil || current != voice {
		a.edit(e.Token(), errHistoryView.Error(), nil)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !a.rememberHistory(v) {
		a.edit(e.Token(), "履歴画面が混み合っています。少し待って再実行してください。", nil)
		return
	}
	a.showHistory(e.Token(), v)
}

func ephemeral(text string) discord.MessageCreate {
	return discord.MessageCreate{Content: text, Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}}
}

func (a *App) historyReply(token, text string) {
	_, err := a.client.Rest.CreateFollowupMessage(a.config.ApplicationID, token, ephemeral(text))
	a.logOperationFailure("history.reply", err)
}

func (a *App) historyComponent(e *events.ComponentInteractionCreate) {
	v, action, err := a.historyAction(e.Data.CustomID(), e.User().ID, e.GuildID(), e.Message.ID)
	if err != nil {
		a.logOperationFailure("history.reject", e.CreateMessage(ephemeral(err.Error())))
		return
	}
	defer v.mu.Unlock()
	if action == "jump" || action == "retry" {
		if len(v.rows) == 0 {
			a.logOperationFailure("history.empty_page", e.CreateMessage(ephemeral("このVCの再生履歴はまだありません。")))
			return
		}
		submit := "submit"
		if action == "retry" {
			submit = "resubmit"
		}
		modal := v.pageModal(submit)
		a.logOperationFailure("history.modal", e.Modal(modal))
		return
	}
	if err = e.DeferUpdateMessage(); err != nil {
		a.logOperationFailure("history.defer", err)
		return
	}
	if action == "add" || action == "front" {
		a.readdHistory(e, v, action == "front")
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	snap, page := v.snapshot, v.page
	switch action {
	case "prev":
		page--
	case "next":
		page++
	case "refresh":
		snap, err = a.historyStore.Snapshot(ctx, v.guild.String(), v.voice.String())
		page = 1
	case "select":
		data, ok := e.Data.(discord.StringSelectMenuInteractionData)
		if !ok || len(data.Values) != 1 {
			a.historyReply(e.Token(), errHistoryView.Error())
			return
		}
		id, parseErr := strconv.ParseInt(data.Values[0], 10, 64)
		if parseErr != nil || !v.selectRecord(id) {
			a.historyReply(e.Token(), errHistoryView.Error())
			return
		}
		if _, err = a.historyStore.Page(ctx, snap, page); err != nil {
			v.selected = 0
			a.historyReply(e.Token(), historyProblem(err))
			return
		}
		if current, checkErr := a.historyVoice(v.guild, v.owner); checkErr != nil || current != v.voice {
			a.historyReply(e.Token(), errHistoryView.Error())
			return
		}
		a.showHistory(e.Token(), v)
		return
	default:
		a.historyReply(e.Token(), errHistoryView.Error())
		return
	}
	if err == nil {
		err = v.load(ctx, a.historyStore, snap, page)
	}
	if err != nil {
		a.logOperationFailure("history.page", err)
		a.historyReply(e.Token(), historyProblem(err))
		return
	}
	if current, checkErr := a.historyVoice(v.guild, v.owner); checkErr != nil || current != v.voice {
		a.historyReply(e.Token(), errHistoryView.Error())
		return
	}
	a.showHistory(e.Token(), v)
}

func (a *App) onHistoryModal(e *events.ModalSubmitInteractionCreate) {
	if !strings.HasPrefix(e.Data.CustomID, "history:") {
		return
	}
	message := snowflake.ID(0)
	if e.Message != nil {
		message = e.Message.ID
	}
	v, action, err := a.historyAction(e.Data.CustomID, e.User().ID, e.GuildID(), message)
	if err != nil {
		a.logOperationFailure("history.reject_modal", e.CreateMessage(ephemeral(err.Error())))
		return
	}
	defer v.mu.Unlock()
	page, err := strconv.Atoi(strings.TrimSpace(e.Data.Text("page")))
	if action != "submit" && action != "resubmit" {
		a.logOperationFailure("history.invalid_modal", e.CreateMessage(ephemeral(errHistoryView.Error())))
		return
	}
	if err != nil || page < 1 || int64(page) > v.pageCount() {
		if err := e.DeferCreateMessage(true); err != nil {
			a.logOperationFailure("history.invalid_page", err)
			return
		}
		v.retryMessage = 0
		if m := a.edit(e.Token(), fmt.Sprintf("1〜%dの整数を入力してください。", v.pageCount()), []discord.LayoutComponent{discord.NewActionRow(discord.NewSecondaryButton("入力し直す", v.componentID("retry")))}); m != nil {
			v.retryMessage = m.ID
		}
		return
	}
	if err = e.DeferUpdateMessage(); err != nil {
		a.logOperationFailure("history.defer_modal", err)
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	if err = v.load(ctx, a.historyStore, v.snapshot, page); err != nil {
		a.historyReply(e.Token(), historyProblem(err))
		return
	}
	if current, checkErr := a.historyVoice(v.guild, v.owner); checkErr != nil || current != v.voice {
		a.historyReply(e.Token(), errHistoryView.Error())
		return
	}
	if action == "resubmit" {
		if a.showHistory(v.originalToken, v) {
			a.edit(e.Token(), "指定したページを表示しました。", nil)
		} else {
			a.edit(e.Token(), "履歴画面を更新できませんでした。/historyを実行し直してください。", nil)
		}
	} else {
		a.showHistory(e.Token(), v)
	}
}

func (v *historyView) pageCount() int64 {
	return max(int64(1), (v.snapshot.Count+history.PageSize-1)/history.PageSize)
}

func (v *historyView) pageModal(action string) discord.ModalCreate {
	return discord.NewModalCreate(v.componentID(action), "ページを指定", discord.NewLabel(fmt.Sprintf("ページ番号（1〜%d）", v.pageCount()), discord.NewShortTextInput("page").WithValue(strconv.Itoa(v.page))))
}

func (a *App) readdHistory(e *events.ComponentInteractionCreate, v *historyView, front bool) {
	if v.selected == 0 {
		a.historyReply(e.Token(), "再追加する曲を選択してください。")
		return
	}
	s := a.getSession(v.guild)
	if _, err := s.authorize(v.owner); err != nil {
		a.historyReply(e.Token(), userError(err))
		return
	}
	epoch := s.version()
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	if _, err := a.historyStore.Page(ctx, v.snapshot, v.page); err != nil {
		a.historyReply(e.Token(), historyProblem(err))
		return
	}
	r, err := a.historyStore.Get(ctx, v.guild.String(), v.voice.String(), v.selected)
	if err != nil {
		a.historyReply(e.Token(), historyProblem(err))
		return
	}
	input, err := source.Parse(r.Entry.Track.URL)
	if err != nil || input.Playlist {
		a.historyReply(e.Token(), "この履歴の曲は再追加できません。")
		return
	}
	result, err := a.source.Inspect(ctx, input.URL)
	if err != nil || len(result.Tracks) != 1 {
		a.historyReply(e.Token(), source.AuthMessage(err, "曲を取得できませんでした。キューには追加していません。"))
		return
	}
	voice, err := a.historyVoice(v.guild, v.owner)
	if err != nil || voice != v.voice || time.Now().After(v.expires) {
		a.historyReply(e.Token(), errHistoryView.Error())
		return
	}
	n, _, addErr := s.acceptAt(v.owner, music.Requester{ID: v.owner.String(), Name: e.User().Username}, result.Tracks, "", "", e.Channel().ID(), epoch, music.Insertion{Front: front})
	text := "キューに追加しました。"
	if front {
		text = "自分のキューの先頭に追加しました。"
	}
	if n > 0 {
		v.selected = 0
		if current, checkErr := a.historyVoice(v.guild, v.owner); checkErr == nil && current == v.voice && time.Now().Before(v.expires) {
			a.showHistory(e.Token(), v)
		}
		if addErr != nil {
			text += "\n" + userError(addErr)
		}
	} else if addErr != nil {
		text = userError(addErr)
	} else {
		text = "曲を追加できませんでした。"
	}
	a.historyReply(e.Token(), text)
}
