package discordbot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"asterinmusic/internal/authorization"
	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

var errQueueEditor = errors.New("この編集画面は利用できません。/queue showから開き直してください。")

type queueEditorResultError string

func (e queueEditorResultError) Error() string { return string(e) }

func (s *session) editorScopeLocked() (snowflake.ID, uint64) {
	if s.conn != nil {
		s.syncVoiceLocked()
	}
	voice := s.channel
	if voice == 0 {
		voice = s.accessVoice
	}
	return voice, s.epoch
}

func (a *App) queueEditorScope(v *queueEditor) error {
	s := a.getSession(v.guild)
	s.mu.Lock()
	voice, epoch := s.editorScopeLocked()
	stopped := s.shuttingDown
	s.mu.Unlock()
	state, ok := a.client.Caches.VoiceState(v.guild, v.owner)
	if stopped || voice == 0 || voice != v.voice || epoch != v.sessionEpoch || !ok || state.ChannelID == nil || *state.ChannelID != voice || !time.Now().Before(v.expires) {
		return errQueueEditor
	}
	return nil
}

// One fresh member lookup supplies the display batch; evaluate every actual
// item so item-specific policies cannot be bypassed by choosing another owner.
func (a *App) loadQueueEditor(v *queueEditor, follow bool) error {
	if err := a.queueEditorScope(v); err != nil {
		return err
	}
	s := a.getSession(v.guild)
	s.mu.Lock()
	_, pending := s.queue.Snapshot()
	sessionID := s.historySessionID
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	r, err := a.authorizationRequest(ctx, v.owner, authorization.QueueView, authorizationTarget{guild: v.guild, voice: v.voice, session: sessionID})
	if err != nil {
		return err
	}
	d, err := a.authorizer.Authorize(ctx, r)
	if err != nil {
		return errPermissionFacts
	}
	if !d.Allowed {
		return errPermission
	}
	permissions := make(map[string]queueEditPermission, len(pending))
	groups := map[string][]music.Entry{}
	var ownerOrder []string
	for _, entry := range pending {
		if _, ok := groups[entry.Requester.ID]; !ok {
			ownerOrder = append(ownerOrder, entry.Requester.ID)
		}
		groups[entry.Requester.ID] = append(groups[entry.Requester.ID], entry)
		r.Resource.RequesterID, r.Resource.EntryID = entry.Requester.ID, entry.ID
		p := queueEditPermission{}
		for _, action := range []authorization.Action{authorization.QueueRemove, authorization.QueueReorder} {
			r.Action = action
			d, err = a.authorizer.Authorize(ctx, r)
			if err != nil {
				return errPermissionFacts
			}
			if action == authorization.QueueRemove {
				p.remove = d.Allowed
			} else {
				p.reorder = d.Allowed
			}
		}
		permissions[entry.ID] = p
	}
	var owners []queueEditOwner
	for _, owner := range ownerOrder {
		entries := groups[owner]
		if slices.ContainsFunc(entries, func(e music.Entry) bool { p := permissions[e.ID]; return p.remove || p.reorder }) {
			owners = append(owners, queueEditOwner{requester: entries[0].Requester, count: len(entries)})
		}
	}
	if err := a.queueEditorScope(v); err != nil {
		return err
	}
	v.permissions, v.owners = permissions, owners
	if !slices.ContainsFunc(owners, func(o queueEditOwner) bool { return o.requester.ID == v.requester }) {
		v.requester, v.selected = "", ""
		v.page = 1
		if len(owners) > 0 {
			v.requester = owners[0].requester.ID
		}
		if slices.ContainsFunc(owners, func(o queueEditOwner) bool { return o.requester.ID == v.owner.String() }) {
			v.requester = v.owner.String()
		}
	}
	v.entries = groups[v.requester]
	if p := permissions[v.selected]; !p.remove && !p.reorder {
		v.selected = ""
	}
	v.reconcile(follow)
	return nil
}

func (a *App) showQueueEditor(response responseRef, v *queueEditor) {
	if v.choosingOwner {
		a.queueEditorOwnerNames(v)
	}
	v.revision++
	e, c := v.render()
	empty := ""
	embeds := []discord.Embed{e}
	m, err := a.client.Rest.UpdateInteractionResponse(a.config.ApplicationID, response.token, discord.MessageUpdate{Content: &empty, Embeds: &embeds, Components: &c, AllowedMentions: &discord.AllowedMentions{}})
	if err == nil && m != nil && v.message == 0 {
		a.mu.Lock()
		v.message = m.ID
		a.mu.Unlock()
	}
	a.logInteractionFailure(response.operation, "queue_editor.update", err)
}

// Resolve only the visible page. Display lookup failures do not block editing
// and never change the stable requester ID used for authorization or selection.
func (a *App) queueEditorOwnerNames(v *queueEditor) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	for i := (v.ownerPage - 1) * 10; i < min(v.ownerPage*10, len(v.owners)); i++ {
		o := &v.owners[i]
		id, err := snowflake.Parse(o.requester.ID)
		if err != nil {
			continue
		}
		member, ok := a.client.Caches.Member(v.guild, id)
		if ok {
			o.requester.Name = member.EffectiveName()
			continue
		}
		if a.client.Rest != nil && ctx.Err() == nil {
			if member, err := a.client.Rest.GetMember(v.guild, id, rest.WithCtx(ctx)); err == nil && member != nil {
				o.requester.Name = member.EffectiveName()
			}
		}
	}
}

func (a *App) openQueueEditor(e *events.ComponentInteractionCreate) {
	parts := strings.Split(e.Data.CustomID(), ":")
	if len(parts) != 2 || parts[1] != e.User().ID.String() || e.GuildID() == nil || *e.GuildID() != a.config.GuildID {
		a.logInteractionFailure(e.ID(), "queue_editor.reject", e.CreateMessage(ephemeral(errQueueEditor.Error())))
		return
	}
	if err := e.DeferUpdateMessage(); err != nil {
		a.logInteractionFailure(e.ID(), "queue_editor.defer", err)
		return
	}
	s := a.getSession(*e.GuildID())
	s.mu.Lock()
	voice, epoch := s.editorScopeLocked()
	s.mu.Unlock()
	v := &queueEditor{id: rand.Text(), owner: e.User().ID, guild: *e.GuildID(), voice: voice, message: e.Message.ID, sessionEpoch: epoch, expires: time.Now().Add(queueEditorTTL), page: 1, ownerPage: 1}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := a.loadQueueEditor(v, false); err != nil {
		a.historyReply(responseFor(e), err.Error())
		return
	}
	if !a.rememberQueueEditor(v) {
		a.historyReply(responseFor(e), "編集画面が混み合っています。少し待って開き直してください。")
		return
	}
	a.showQueueEditor(responseFor(e), v)
}

func (a *App) queueEditorAction(custom string, user snowflake.ID, guild *snowflake.ID, message snowflake.ID) (*queueEditor, string, error) {
	p := strings.Split(custom, ":")
	if len(p) != 4 || p[0] != "qedit" || guild == nil || *guild != a.config.GuildID {
		return nil, "", errQueueEditor
	}
	rev, err := strconv.ParseUint(p[2], 10, 64)
	if err != nil {
		return nil, "", errQueueEditor
	}
	a.mu.Lock()
	v := a.queueEditors[p[1]]
	a.mu.Unlock()
	if v == nil {
		return nil, "", errQueueEditor
	}
	if !v.mu.TryLock() {
		return nil, "", errors.New("処理中です。少し待って操作してください。")
	}
	if !v.valid(user, *guild, v.voice, message, rev, time.Now()) || a.queueEditorScope(v) != nil {
		v.mu.Unlock()
		return nil, "", errQueueEditor
	}
	return v, p[3], nil
}

func queueEditProblem(err error) string {
	var result queueEditorResultError
	if errors.As(err, &result) {
		return result.Error()
	}
	switch {
	case errors.Is(err, errQueueEditor):
		return errQueueEditor.Error()
	case errors.Is(err, music.ErrEntryNotWaiting):
		return "この曲は待機キューにありません。一覧を更新しました。"
	case errors.Is(err, music.ErrQueueChanged), errors.Is(err, errQueueChanged):
		return "待機曲の順番が変わりました。一覧を確認して操作し直してください。"
	case errors.Is(err, music.ErrQueuePosition):
		return "表示範囲内の順番を指定してください。"
	default:
		return userError(err)
	}
}

func (a *App) mutateQueueEditor(v *queueEditor, position int, remove bool) error {
	index := v.selectedIndex()
	if index < 0 {
		return music.ErrEntryNotWaiting
	}
	entry := v.entries[index]
	ids := make([]string, len(v.entries))
	for i, e := range v.entries {
		ids[i] = e.ID
	}
	err := a.getSession(v.guild).editQueueScopedAs(v.owner, v.requester, v.selected, position, ids, remove, &queueEditScope{voice: v.voice, epoch: v.sessionEpoch, expires: v.expires})
	if err != nil {
		v.notice = queueEditProblem(err)
	} else if remove {
		v.notice = queueLink(compactQueueTrack(entry.Track)) + "を削除しました。"
		v.selected = ""
	} else {
		v.notice = fmt.Sprintf("%sを、%sの待機曲の%d曲目に移動しました。", queueLink(compactQueueTrack(entry.Track)), queueMention(v.requester), position)
	}
	if loadErr := a.loadQueueEditor(v, err == nil && !remove); loadErr != nil {
		return queueEditorResultError(v.notice + "\n編集画面を更新できませんでした。/queue showから開き直してください。")
	}
	return nil
}

func (a *App) queueEditorComponent(e *events.ComponentInteractionCreate) {
	v, action, err := a.queueEditorAction(e.Data.CustomID(), e.User().ID, e.GuildID(), e.Message.ID)
	if err != nil {
		a.logInteractionFailure(e.ID(), "queue_editor.reject", e.CreateMessage(ephemeral(err.Error())))
		return
	}
	defer v.mu.Unlock()
	if v.clearConfirmation {
		a.queueClearComponent(e, v, action)
		return
	}
	if action == "jump" || action == "move" {
		if v.choosingOwner || (action == "move" && (v.selectedIndex() < 0 || !v.permissions[v.selected].reorder)) {
			a.logInteractionFailure(e.ID(), "queue_editor.reject_modal", e.CreateMessage(ephemeral(errQueueEditor.Error())))
			return
		}
		a.logInteractionFailure(e.ID(), "queue_editor.modal", e.Modal(v.modal(action == "move")))
		return
	}
	if err = e.DeferUpdateMessage(); err != nil {
		a.logInteractionFailure(e.ID(), "queue_editor.defer", err)
		return
	}
	if action == "back" {
		a.mu.Lock()
		delete(a.queueEditors, v.id)
		a.mu.Unlock()
		s := a.getSession(v.guild)
		s.queueViewMu.Lock()
		defer s.queueViewMu.Unlock()
		a.editQueue(responseFor(e), s, v.owner, 1, "")
		return
	}
	v.notice = ""
	mutate := false
	switch action {
	case "up", "down", "front", "remove":
		if v.choosingOwner {
			err = errQueueEditor
			break
		}
		position := v.selectedIndex() + 1
		if action == "up" {
			position--
		}
		if action == "down" {
			position++
		}
		if action == "front" {
			position = 1
		}
		err = a.mutateQueueEditor(v, position, action == "remove")
		mutate = true
	case "prev":
		v.page--
		v.selected = ""
	case "next":
		v.page++
		v.selected = ""
	case "owner_prev":
		v.ownerPage--
	case "owner_next":
		v.ownerPage++
	case "owners":
		v.choosingOwner = true
		v.selected = ""
	case "list":
		v.choosingOwner = false
	case "refresh":
	case "select", "owner":
		data, ok := e.Data.(discord.StringSelectMenuInteractionData)
		if !ok || len(data.Values) != 1 {
			err = errQueueEditor
			break
		}
		if action == "select" {
			if v.choosingOwner || !v.selectEntry(data.Values[0]) {
				err = errQueueEditor
			}
		} else {
			found := false
			for i := (v.ownerPage - 1) * 10; i < min(v.ownerPage*10, len(v.owners)); i++ {
				if v.owners[i].requester.ID == data.Values[0] {
					found = true
				}
			}
			if !v.choosingOwner || !found {
				err = errQueueEditor
				break
			}
			v.requester, v.selected, v.page, v.choosingOwner = data.Values[0], "", 1, false
		}
	default:
		err = errQueueEditor
	}
	if err == nil && !mutate {
		err = a.loadQueueEditor(v, false)
	}
	if err != nil {
		a.historyReply(responseFor(e), queueEditProblem(err))
		v.revision++ // Internal selection/page may have changed; never reuse old buttons.
		return
	}
	a.showQueueEditor(responseFor(e), v)
}

func (a *App) onQueueEditorModal(e *events.ModalSubmitInteractionCreate) {
	if !strings.HasPrefix(e.Data.CustomID, "qedit:") {
		return
	}
	defer a.observeOperation(e.ID(), "modal")()
	message := snowflake.ID(0)
	if e.Message != nil {
		message = e.Message.ID
	}
	v, action, err := a.queueEditorAction(e.Data.CustomID, e.User().ID, e.GuildID(), message)
	if err != nil {
		a.logInteractionFailure(e.ID(), "queue_editor.reject_modal", e.CreateMessage(ephemeral(err.Error())))
		return
	}
	defer v.mu.Unlock()
	if v.clearConfirmation {
		a.logInteractionFailure(e.ID(), "queue_editor.reject_modal", e.CreateMessage(ephemeral(errQueueEditor.Error())))
		return
	}
	n, parseErr := strconv.Atoi(strings.TrimSpace(e.Data.Text("position")))
	limit := v.pageCount()
	if action == "move_submit" {
		limit = len(v.entries)
	}
	if (action != "page_submit" && action != "move_submit") || parseErr != nil || n < 1 || n > limit {
		a.logInteractionFailure(e.ID(), "queue_editor.invalid_input", e.CreateMessage(ephemeral(fmt.Sprintf("1〜%dの整数を入力してください。元の画面のボタンから入力し直せます。", limit))))
		return
	}
	if err = e.DeferUpdateMessage(); err != nil {
		a.logInteractionFailure(e.ID(), "queue_editor.defer_modal", err)
		return
	}
	v.notice = ""
	if action == "move_submit" {
		err = a.mutateQueueEditor(v, n, false)
	} else {
		v.page, v.selected = n, ""
		err = a.loadQueueEditor(v, false)
	}
	if err != nil {
		a.historyReply(responseFor(e), queueEditProblem(err))
		v.revision++
		return
	}
	a.showQueueEditor(responseFor(e), v)
}
