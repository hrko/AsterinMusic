package discordbot

import (
	"asterinmusic/internal/music"
	"asterinmusic/internal/source"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"math"
	"regexp"
	"strconv"
	"strings"
)

func queueText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = truncate(s, 60)
	return escapeText(s)
}
func escapeText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`", "~", "\\~", "|", "\\|", "<", "‹", ">", "›", "@", "＠").Replace(s)
}
func queueLink(t music.Track) string {
	return trackLink(t, 60)
}
func trackLink(t music.Track, limit int) string {
	title := escapeText(truncate(strings.Join(strings.Fields(t.Title), " "), limit))
	if title == "" {
		title = "曲名不明"
	}
	if input, err := source.Parse(t.URL); err == nil && !input.Playlist && len(input.URL) <= 160 {
		return "**[" + title + "](" + input.URL + ")**"
	}
	return "**" + title + "**"
}
func queueMention(id string) string {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 {
		return "不明なユーザー"
	}
	return "<@" + strconv.FormatUint(n, 10) + ">"
}
func queueSeconds(t music.Track) (int64, bool) {
	if t.Duration == nil || math.IsNaN(*t.Duration) || math.IsInf(*t.Duration, 0) || *t.Duration <= 0 || *t.Duration > 1e9 {
		return 0, false
	}
	return int64(*t.Duration), true
}
func queueDuration(seconds int64) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
func queueDetail(t music.Track, who string) string {
	parts := []string{queueTrackDuration(t)}
	if channel := queueChannel(t); channel != "" {
		parts = append(parts, channel)
	} else {
		parts = append(parts, queueSite(t))
	}
	if who != "" {
		parts = append(parts, who)
	}
	return strings.Join(parts, " · ")
}

var channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

func queueChannel(t music.Track) string {
	if !channelIDPattern.MatchString(t.ChannelID) || strings.TrimSpace(t.ChannelTitle) == "" {
		return ""
	}
	return "[" + queueText(truncate(t.ChannelTitle, 24)) + "](https://www.youtube.com/channel/" + t.ChannelID + ")"
}
func trackThumbnail(t music.Track) *discord.EmbedResource {
	if strings.HasPrefix(t.ThumbnailURL, "https://") && len(t.ThumbnailURL) <= 2048 {
		return &discord.EmbedResource{URL: t.ThumbnailURL}
	}
	return nil
}
func queueTrackDuration(t music.Track) string {
	duration := "時間不明"
	if sec, ok := queueSeconds(t); ok {
		duration = queueDuration(sec)
	}
	return duration
}
func queueSite(t music.Track) string {
	return map[string]string{"youtube": "YouTube", "youtube_music": "YouTube Music", "niconico": "ニコニコ動画"}[t.Site]
}
func queueTotalDuration(seconds int64) string {
	if seconds >= 3600 {
		return fmt.Sprintf("%d時間%d分", seconds/3600, seconds/60%60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%d分%d秒", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d秒", seconds)
}

// Read-only snapshot under session.mu. Viewing never fetches or consumes tracks.
func (s *session) queueView(owner snowflake.ID, page int, previous string) (discord.Embed, []discord.LayoutComponent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, pending := s.queue.Snapshot()
	mode := "manual"
	if len(pending) == 0 && s.auto.enabled {
		mode = "auto"
	}
	if previous != "" && previous != mode {
		page = 1
	}
	entries := pending
	if mode == "auto" {
		entries = nil
		for _, t := range s.auto.available(nil) {
			entries = append(entries, music.Entry{Track: t})
		}
	}
	pages := max(1, (len(entries)+9)/10)
	page = min(max(1, page), pages)
	embed := discord.Embed{Title: "再生キュー", Color: colorInfo}
	current := "再生中の曲はありません。"
	label := "再生中"
	if s.current != nil {
		who := queueMention(s.current.Requester.ID)
		if s.currentAuto {
			who = "自動再生"
		}
		current = queueLink(s.current.Track) + "\n" + queueDetail(s.current.Track, who)
		embed.Thumbnail = trackThumbnail(s.current.Track)
		if !s.started {
			label = "再生準備中"
		}
	}
	var description strings.Builder
	fmt.Fprintf(&description, "**%s**\n%s\n\n", label, current)
	heading := "次の再生予定"
	if mode == "auto" {
		heading = "次の候補"
		commonSite := ""
		for i := (page - 1) * 10; i < min(page*10, len(entries)); i++ {
			site := queueSite(entries[i].Track)
			if i == (page-1)*10 {
				commonSite = site
			} else if commonSite != site {
				commonSite = ""
				break
			}
		}
		if commonSite != "" {
			heading += " · " + commonSite
		}
	}
	fmt.Fprintf(&description, "**%s**\n", heading)
	if len(entries) == 0 {
		empty := "待機中の曲はありません。"
		if mode == "auto" {
			empty = "表示できる候補はまだありません。"
		}
		description.WriteString(empty)
	} else {
		for i := (page - 1) * 10; i < min(page*10, len(entries)); i++ {
			t := entries[i].Track
			who := ""
			if mode == "manual" {
				who = queueMention(entries[i].Requester.ID)
			}
			number := fmt.Sprintf("%d. ", i+1)
			fmt.Fprintf(&description, "%s%s\n%s%s\n", number, queueLink(t), strings.Repeat(" ", len(number)), queueDetail(t, who))
		}
	}
	embed.Description = strings.TrimSpace(description.String())
	state := "OFF"
	if s.auto.enabled {
		state = "ON"
		if s.auto.seed != nil && mode == "auto" {
			state += "\n起点：" + queueLink(*s.auto.seed)
		}
		switch {
		case len(pending) > 0:
			state += "\n待機キュー終了後に自動再生します。"
		case s.auto.seed == nil:
			state += "\n起点となるYouTube曲の再生待ちです。"
		case s.auto.fetching:
			state += "\n候補を取得中です。"
		case len(entries) > 0:
		case s.auto.attempts > 0:
			state += "\n候補を取得できませんでした。"
		default:
			state += "\n候補は未取得です。"
		}
	}
	status, detail, _ := strings.Cut(state, "\n")
	if detail == "" {
		embed.Description += "\n\n**自動再生 " + status + "**"
	} else {
		embed.Fields = append(embed.Fields, discord.EmbedField{Name: "自動再生 " + status, Value: detail})
	}
	footer := fmt.Sprintf("%dページ目 · 取得済みの候補\n候補は変更・補充されます。", page)
	if mode == "manual" {
		var total int64
		unknown := 0
		for _, e := range pending {
			if sec, ok := queueSeconds(e.Track); ok {
				total += sec
			} else {
				unknown++
			}
		}
		footer = fmt.Sprintf("待機%d曲 · 合計%s", len(pending), queueTotalDuration(total))
		if unknown > 0 {
			footer = fmt.Sprintf("待機%d曲 · 既知分%s · 時間不明%d曲", len(pending), queueTotalDuration(total), unknown)
		}
		footer += fmt.Sprintf("\n%d / %dページ · 表示時点の再生予定順", page, pages)
	}
	embed.Footer = &discord.EmbedFooter{Text: footer}
	id := func(p int) string { return fmt.Sprintf("queue:1:%s:%s:%d", owner, mode, p) }
	buttons := []discord.LayoutComponent{discord.NewActionRow(discord.NewSecondaryButton("◀ 前へ", id(page-1)).WithDisabled(page == 1), discord.NewSecondaryButton("次へ ▶", id(page+1)).WithDisabled(page == pages), discord.NewSecondaryButton("↻ 更新", id(page)))}
	return embed, buttons
}

func (a *App) editQueue(token string, s *session, owner snowflake.ID, page int, previous string) {
	embed, components := s.queueView(owner, page, previous)
	embeds := []discord.Embed{embed}
	empty := ""
	_, _ = a.client.Rest.UpdateInteractionResponse(a.config.ApplicationID, token, discord.MessageUpdate{Content: &empty, Embeds: &embeds, Components: &components, AllowedMentions: &discord.AllowedMentions{}})
}
func (a *App) queueComponent(e *events.ComponentInteractionCreate) {
	parts := strings.Split(e.Data.CustomID(), ":")
	fail := func() {
		_ = e.CreateMessage(discord.MessageCreate{Content: "この操作は利用できません。/queueを実行し直してください。", Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}})
	}
	if e.GuildID() == nil || *e.GuildID() != a.config.GuildID || len(parts) != 5 || parts[1] != "1" || parts[2] != e.User().ID.String() || (parts[3] != "auto" && parts[3] != "manual") {
		fail()
		return
	}
	page, err := strconv.Atoi(parts[4])
	if err != nil || page < 1 || page > 1000000 {
		fail()
		return
	}
	if err = e.DeferUpdateMessage(); err != nil {
		return
	}
	s := a.getSession(*e.GuildID())
	s.queueViewMu.Lock()
	defer s.queueViewMu.Unlock()
	a.editQueue(e.Token(), s, e.User().ID, page, parts[3])
}
