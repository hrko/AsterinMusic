package main

import (
	"fmt"
	"strings"

	"github.com/clipperhouse/displaywidth"
)

func widthSample(variant string) embed {
	titleLimit, channelLimit := 56, 24
	label := "B：標準・曲名56幅／チャンネル24幅"
	if variant == "width-compact" {
		titleLimit, channelLimit = 40, 18
		label = "C：コンパクト・曲名40幅／チャンネル18幅"
	}
	if variant == "width-current" {
		titleLimit, channelLimit = 60, 24
		label = "A：現行・曲名60文字／チャンネル24文字"
	}
	shorten := func(s string, limit int) string {
		s = strings.Join(strings.Fields(s), " ")
		if variant == "width-current" {
			r := []rune(s)
			if len(r) > limit {
				return string(r[:limit-1]) + "…"
			}
			return s
		}
		return (displaywidth.Options{EastAsianWidth: false}).TruncateString(s, limit, "…")
	}
	escape := func(s string) string {
		return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(s)
	}
	titles := []string{
		"【Lyric Video】結束バンド「星座になれたら」／ TVアニメ「ぼっち・ざ・ろっく！」第12話劇中曲",
		"転がる岩、君に朝が降る",
		"藤井 風 - 青春病 (Live at Nissan Stadium) [Day2] — 長いタイトルの折り返し確認",
		"A Beautiful Night Under the Stars — Live Performance at the Summer Music Festival",
		"ひみつ基地",
		"Café / Cafe\u0301 — Une chanson pour une très longue soirée d’été",
		"별이 빛나는 밤에 함께 듣는 노래 — 라이브 공연 스페셜 버전",
		"🎵 Music for Everyone 👨‍👩‍👧‍👦 🌈 — A Long Evening Playlist with Friends 🇯🇵🇫🇷",
		"【Lyric Video】結束バンド「光の中へ」／ TVアニメ「ぼっち・ざ・ろっく！」",
		"青い春と西の空",
	}
	channels := []string{"アニプレックス チャンネル", "kessoku band - Topic", "Fujii Kaze", "The International Live Music Channel"}
	e := embed{Title: label, Color: 0x527DA8, Footer: map[string]string{"text": "表示検証用 · 多言語・絵文字の一部は架空の曲名 · 再生しません"}}
	e.Description = "**再生中**\n**藤井 風 - きらり (Official Video)**\n3:52 · Fujii Kaze · サンプルユーザー\n\n**次の再生予定**\n"
	for i, title := range titles {
		fmtTitle := escape(shorten(title, titleLimit))
		channel := escape(shorten(channels[i%len(channels)], channelLimit))
		// Preview links use the homepage, not unrelated individual videos.
		e.Description += fmt.Sprintf("%d. **[%s](https://www.youtube.com/)**  \n   -# 3:52 · [%s](https://www.youtube.com/) · サンプルユーザー\n", i+1, fmtTitle, channel)
	}
	e.Description = strings.TrimSpace(e.Description)
	e.Fields = []field{{Name: "自動再生 ON", Value: "待機キュー終了後に自動再生します。"}}
	return e
}
