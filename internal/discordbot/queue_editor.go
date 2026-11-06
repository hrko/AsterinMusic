package discordbot

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"asterinmusic/internal/music"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

const queueEditorTTL = 15 * time.Minute
const queueEditorLimit = 1024

type queueEditPermission struct{ remove, reorder bool }
type queueEditOwner struct {
	requester music.Requester
	count     int
}

// Each view is private and bound to one Discord message and playback scope.
// Queue entries and permission flags are display snapshots, never capabilities.
type queueEditor struct {
	mu                           sync.Mutex
	id                           string
	owner, guild, voice, message snowflake.ID
	expires                      time.Time
	revision                     uint64
	sessionEpoch                 uint64
	requester, selected          string
	page, ownerPage              int
	choosingOwner                bool
	entries                      []music.Entry
	owners                       []queueEditOwner
	permissions                  map[string]queueEditPermission
	notice                       string
}

func (v *queueEditor) componentID(action string) string {
	return fmt.Sprintf("qedit:%s:%d:%s", v.id, v.revision, action)
}

func (v *queueEditor) valid(owner, guild, voice, message snowflake.ID, revision uint64, now time.Time) bool {
	return owner == v.owner && guild == v.guild && voice == v.voice && message == v.message && revision == v.revision && now.Before(v.expires)
}

func (a *App) rememberQueueEditor(v *queueEditor) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queueEditors == nil {
		a.queueEditors = make(map[string]*queueEditor)
	}
	for id, old := range a.queueEditors {
		if !time.Now().Before(old.expires) || (v.message != 0 && old.message == v.message && old.guild == v.guild && old.owner == v.owner) {
			delete(a.queueEditors, id)
		}
	}
	if len(a.queueEditors) >= queueEditorLimit {
		return false
	}
	a.queueEditors[v.id] = v
	return true
}

func (v *queueEditor) pageCount() int      { return max(1, (len(v.entries)+9)/10) }
func (v *queueEditor) ownerPageCount() int { return max(1, (len(v.owners)+9)/10) }
func (v *queueEditor) selectedIndex() int {
	return slices.IndexFunc(v.entries, func(e music.Entry) bool { return e.ID == v.selected })
}

// reconcile keeps a moved selection visible, but refresh never selects an
// off-page item implicitly. Explicit navigation clears selection before this.
func (v *queueEditor) reconcile(follow bool) {
	v.page = min(max(v.page, 1), v.pageCount())
	v.ownerPage = min(max(v.ownerPage, 1), v.ownerPageCount())
	index := v.selectedIndex()
	if index < 0 {
		v.selected = ""
		return
	}
	if follow {
		v.page = index/10 + 1
	}
	if index/10+1 != v.page {
		v.selected = ""
	}
}

func (v *queueEditor) selectEntry(id string) bool {
	for i := (v.page - 1) * 10; i < min(v.page*10, len(v.entries)); i++ {
		if v.entries[i].ID == id {
			p := v.permissions[id]
			if !p.remove && !p.reorder {
				return false
			}
			v.selected = id
			return true
		}
	}
	return false
}

func (v *queueEditor) modal(move bool) discord.ModalCreate {
	action, title, label, value := "page_submit", "ページを指定", fmt.Sprintf("ページ番号（1〜%d）", v.pageCount()), v.page
	if move {
		action, title, label, value = "move_submit", "順番を指定", fmt.Sprintf("この追加者の移動後の順番（1〜%d）", len(v.entries)), v.selectedIndex()+1
	}
	return discord.NewModalCreate(v.componentID(action), title, discord.NewLabel(label, discord.NewShortTextInput("position").WithValue(strconv.Itoa(value))))
}

func (v *queueEditor) render() (discord.Embed, []discord.LayoutComponent) {
	if v.choosingOwner {
		return v.renderOwners()
	}
	body := "**" + queueMention(v.requester) + " の待機曲**\n順番の変更は、この追加者の待機曲内に適用されます。\n\n"
	if v.requester == "" {
		body = "編集できる待機曲はありません。\n"
	}
	indent := strings.Repeat(" ", len(fmt.Sprintf("%d. ", (v.page-1)*10+1)))
	var options []discord.StringSelectMenuOption
	for i := (v.page - 1) * 10; i < min(v.page*10, len(v.entries)); i++ {
		e := v.entries[i]
		t := compactQueueTrack(e.Track)
		marker := ""
		if e.ID == v.selected {
			marker = "▶ "
		}
		body += fmt.Sprintf("%d. %s%s  \n%s-# %s\n", i+1, marker, queueLink(t), indent, queueDetail(t, ""))
		p := v.permissions[e.ID]
		if p.remove || p.reorder {
			options = append(options, discord.NewStringSelectMenuOption(truncate(fmt.Sprintf("%d. %s", i+1, e.Track.Title), 90), e.ID).WithDefault(e.ID == v.selected))
		}
	}
	if len(v.entries) == 0 && v.requester != "" {
		body += "待機中の曲はありません。"
	}
	e := discord.Embed{Title: "キューを編集", Description: strings.TrimSuffix(body, "\n"), Color: colorInfo, Footer: &discord.EmbedFooter{Text: fmt.Sprintf("待機%d曲 · 表示時点の追加者内の順番\n操作は作成から15分間有効", len(v.entries))}}
	index := v.selectedIndex()
	if index >= 0 {
		e.Fields = append(e.Fields, discord.EmbedField{Name: "▶ 操作対象", Value: queueLink(compactQueueTrack(v.entries[index].Track))})
	}
	if v.notice != "" {
		e.Fields = append(e.Fields, discord.EmbedField{Name: "操作結果", Value: v.notice})
	}
	p := v.permissions[v.selected]
	empty := len(options) == 0
	if empty {
		options = append(options, discord.NewStringSelectMenuOption("選択できる曲はありません", "none"))
	}
	components := []discord.LayoutComponent{
		discord.NewActionRow(discord.NewStringSelectMenu(v.componentID("select"), "操作する曲を選択", options...).WithDisabled(empty)),
		discord.NewActionRow(
			discord.NewSecondaryButton("↑ 上へ", v.componentID("up")).WithDisabled(!p.reorder || index <= 0),
			discord.NewSecondaryButton("↓ 下へ", v.componentID("down")).WithDisabled(!p.reorder || index < 0 || index == len(v.entries)-1),
			discord.NewSecondaryButton("先頭へ", v.componentID("front")).WithDisabled(!p.reorder || index <= 0),
			discord.NewSecondaryButton("順番を指定", v.componentID("move")).WithDisabled(!p.reorder || index < 0 || len(v.entries) < 2),
			discord.NewDangerButton("削除", v.componentID("remove")).WithDisabled(!p.remove || index < 0)),
		discord.NewActionRow(
			discord.NewSecondaryButton("◀ 前へ", v.componentID("prev")).WithDisabled(v.page <= 1),
			discord.NewSecondaryButton(fmt.Sprintf("%d / %d", v.page, v.pageCount()), v.componentID("jump")).WithDisabled(len(v.entries) == 0),
			discord.NewSecondaryButton("次へ ▶", v.componentID("next")).WithDisabled(v.page >= v.pageCount()),
			discord.NewSecondaryButton("↻ 更新", v.componentID("refresh"))),
	}
	buttons := []discord.InteractiveComponent{}
	if slices.ContainsFunc(v.owners, func(o queueEditOwner) bool { return o.requester.ID != v.owner.String() }) {
		buttons = append(buttons, discord.NewSecondaryButton("追加者を変更", v.componentID("owners")))
	}
	buttons = append(buttons, discord.NewSecondaryButton("再生キューへ戻る", v.componentID("back")))
	components = append(components, discord.NewActionRow(buttons...))
	return e, components
}

func (v *queueEditor) renderOwners() (discord.Embed, []discord.LayoutComponent) {
	body := "編集する待機曲の追加者を選んでください。\n\n"
	var options []discord.StringSelectMenuOption
	for i := (v.ownerPage - 1) * 10; i < min(v.ownerPage*10, len(v.owners)); i++ {
		o := v.owners[i]
		body += fmt.Sprintf("- **%s** · 待機%d曲\n", queueMention(o.requester.ID), o.count)
		name := o.requester.Name
		if name == "" {
			name = "ユーザー"
		}
		options = append(options, discord.NewStringSelectMenuOption(truncate(name, 80), o.requester.ID).WithDescription(fmt.Sprintf("待機%d曲 · ID: %s", o.count, o.requester.ID)))
	}
	empty := len(options) == 0
	if empty {
		body += "編集できる追加者はいません。"
		options = append(options, discord.NewStringSelectMenuOption("追加者なし", "none"))
	}
	e := discord.Embed{Title: "編集する追加者を選択", Description: strings.TrimSuffix(body, "\n"), Color: colorInfo, Footer: &discord.EmbedFooter{Text: fmt.Sprintf("%d / %dページ · 操作は作成から15分間有効", v.ownerPage, v.ownerPageCount())}}
	return e, []discord.LayoutComponent{
		discord.NewActionRow(discord.NewStringSelectMenu(v.componentID("owner"), "追加者を選択", options...).WithDisabled(empty)),
		discord.NewActionRow(discord.NewSecondaryButton("◀ 前へ", v.componentID("owner_prev")).WithDisabled(v.ownerPage <= 1), discord.NewSecondaryButton("次へ ▶", v.componentID("owner_next")).WithDisabled(v.ownerPage >= v.ownerPageCount()), discord.NewSecondaryButton("↻ 更新", v.componentID("refresh"))),
		discord.NewActionRow(discord.NewSecondaryButton("編集一覧へ戻る", v.componentID("list"))),
	}
}
