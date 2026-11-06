package discordbot

import (
	"crypto/rand"
	"fmt"
	"slices"
	"time"

	"asterinmusic/internal/authorization"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

// All queue command responses, including clear confirmation/results, are private.
func (a *App) queueCommand(e *events.ApplicationCommandInteractionCreate, s *session) {
	d := e.SlashCommandInteractionData()
	if d.SubCommandName == nil {
		a.edit(responseFor(e), "/queue show・edit・clearを選んでください。", nil)
		return
	}
	if *d.SubCommandName == "show" {
		s.queueViewMu.Lock()
		defer s.queueViewMu.Unlock()
		a.editQueue(responseFor(e), s, e.User().ID, max(1, d.Int("page")), "")
		return
	}
	if *d.SubCommandName != "edit" && *d.SubCommandName != "clear" {
		return
	}
	s.mu.Lock()
	voice, epoch := s.editorScopeLocked()
	s.mu.Unlock()
	v := &queueEditor{id: rand.Text(), owner: e.User().ID, guild: s.guild, voice: voice, sessionEpoch: epoch, expires: time.Now().Add(queueEditorTTL), page: 1, ownerPage: 1}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := a.queueEditorScope(v); err != nil {
		a.edit(responseFor(e), "対象の音声チャンネルに参加してから操作してください。", nil)
		return
	}
	if *d.SubCommandName == "clear" {
		if err := s.lockAuthorized(v.owner, authorization.QueueClear); err != nil {
			a.edit(responseFor(e), userError(err), nil)
			return
		}
		_, v.entries = s.queue.Snapshot()
		s.mu.Unlock()
		if len(v.entries) == 0 {
			a.edit(responseFor(e), "消去する待機曲はありません。", nil)
			return
		}
		v.clearConfirmation = true
	} else {
		requested := d.Snowflake("user")
		if requested != 0 {
			v.requester = requested.String()
		}
		if err := a.loadQueueEditor(v, false); err != nil {
			a.edit(responseFor(e), queueEditProblem(err), nil)
			return
		}
		if requested != 0 && v.requester != requested.String() {
			a.edit(responseFor(e), "指定した追加者の編集可能な待機曲がありません。待機曲と権限を確認してください。", nil)
			return
		}
	}
	if !a.rememberQueueEditor(v) {
		a.edit(responseFor(e), "編集画面が混み合っています。少し待って開き直してください。", nil)
		return
	}
	if v.clearConfirmation {
		m := a.editEmbed(responseFor(e), discord.Embed{Title: "全員の待機曲を消去しますか？", Description: fmt.Sprintf("他の人が追加した曲も含め、全員の待機%d曲を消去します。\n再生中の曲と自動再生設定は維持します。", len(v.entries)), Color: colorWarning, Footer: &discord.EmbedFooter{Text: "本人のみ操作可能 · 15分間有効"}}, []discord.LayoutComponent{discord.NewActionRow(discord.NewDangerButton(fmt.Sprintf("全員の待機%d曲を消去", len(v.entries)), v.componentID("clear_confirm")), discord.NewSecondaryButton("キャンセル", v.componentID("clear_cancel")))})
		if m != nil {
			a.mu.Lock()
			v.message = m.ID
			a.mu.Unlock()
		}
	} else {
		a.showQueueEditor(responseFor(e), v)
	}
}

// Clear only the exact set the user confirmed. New additions or started tracks
// require a fresh confirmation rather than silently changing the scope.
func (s *session) clearConfirmedQueue(v *queueEditor) error {
	if err := s.lockAuthorized(v.owner, authorization.QueueClear); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return err
	}
	voice, epoch := s.editorScopeLocked()
	state, ok := s.app.client.Caches.VoiceState(s.guild, v.owner)
	if voice != v.voice || epoch != v.sessionEpoch || !time.Now().Before(v.expires) || !ok || state.ChannelID == nil || *state.ChannelID != voice {
		return errQueueEditor
	}
	_, pending := s.queue.Snapshot()
	ids := func(entries []music.Entry) []string {
		result := make([]string, len(entries))
		for i := range entries {
			result[i] = entries[i].ID
		}
		slices.Sort(result)
		return result
	}
	if !slices.Equal(ids(pending), ids(v.entries)) {
		return errQueueChanged
	}
	s.epoch++
	var err error
	s.queueIOLocked(func() { s.queue.Clear(); err = s.queue.PersistenceError() })
	s.prefetchLocked()
	if err != nil {
		return queueEditorResultError("メモリ上の待機キューは消去しましたが、保存に失敗しました。再起動前にログを確認してください。")
	}
	return nil
}

func (a *App) queueClearComponent(e *events.ComponentInteractionCreate, v *queueEditor, action string) {
	if action != "clear_confirm" && action != "clear_cancel" {
		a.logInteractionFailure(e.ID(), "queue.clear.reject", e.CreateMessage(ephemeral(errQueueEditor.Error())))
		return
	}
	if err := e.DeferUpdateMessage(); err != nil {
		a.logInteractionFailure(e.ID(), "queue.clear.defer", err)
		return
	}
	text := "全消去をキャンセルしました。"
	if action == "clear_confirm" {
		if err := a.getSession(v.guild).clearConfirmedQueue(v); err != nil {
			text = queueEditProblem(err) + "\n必要なら /queue clear から確認し直してください。"
		} else {
			text = "全員の待機曲を消去しました。再生中の曲と自動再生設定は維持しています。"
		}
	}
	a.mu.Lock()
	delete(a.queueEditors, v.id)
	a.mu.Unlock()
	a.edit(responseFor(e), text, nil)
}
