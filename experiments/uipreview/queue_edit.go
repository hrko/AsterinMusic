package main

import (
	"fmt"
	"strings"

	"github.com/clipperhouse/displaywidth"
)

// Static previews: controls are explicitly textual, never live queue actions.
func queueEditSample(variant string) embed {
	e := embed{Title: "キューを編集", Color: 0x527DA8, Footer: map[string]string{
		"text": "表示確認用サンプル · 実際のキューは変更しません",
	}}
	if variant == "queue-edit-owners" {
		e.Title = "編集する追加者を選択"
		e.Description = "編集する待機曲の追加者を選んでください。\n\n" +
			"- **Atule** · 待機23曲\n- **サンプル参加者A** · 待機8曲\n- **サンプル参加者B** · 待機105曲"
		e.Fields = []field{{Name: "操作部品の配置案（テキスト・操作不可）", Value: "[ 追加者を選択 ▾ ]\n[ ◀ 前へ ] [ 1 / 1 ] [ 次へ ▶ ]\n[ 編集一覧へ戻る ]"}}
		return e
	}
	start, selected, total := 1, 10, 23
	if variant == "queue-edit-11" {
		start, selected = 11, 12
	}
	if variant == "queue-edit-91" {
		start, selected, total = 91, 100, 105
	}
	titles := []string{
		"【Lyric Video】結束バンド「星座になれたら」／ TVアニメ「ぼっち・ざ・ろっく！」第12話劇中曲",
		"藤井 風 - 青春病 (Live at Nissan Stadium) [Day2]",
		"転がる岩、君に朝が降る", "ひみつ基地", "Café / Cafe\u0301 — Une chanson pour une très longue soirée d’été",
	}
	short := func(s string, width int) string {
		return (displaywidth.Options{EastAsianWidth: false}).TruncateString(s, width, "…")
	}
	e.Description = "**サンプルユーザー の待機曲**\n順番の変更は、この追加者の待機曲内に適用されます。\n\n"
	indent := strings.Repeat(" ", len(fmt.Sprintf("%d. ", start)))
	selectedTitle := ""
	for i := 0; i < 10; i++ {
		n := start + i
		title := short(titles[i%len(titles)], 56)
		marker := ""
		if n == selected {
			marker, selectedTitle = "▶ ", title
		}
		e.Description += fmt.Sprintf("%d. %s**[%s](https://www.youtube.com/)**  \n%s-# 3:52 · [チャンネル名](https://www.youtube.com/)\n", n, marker, title, indent)
	}
	e.Description = strings.TrimSuffix(e.Description, "\n")
	e.Fields = []field{
		{Name: "▶ 操作対象", Value: fmt.Sprintf("**[%s](https://www.youtube.com/)** · %d曲目", selectedTitle, selected)},
		{Name: "操作部品の配置案（テキスト・操作不可）", Value: fmt.Sprintf(
			"[ 操作する曲を選択 ▾ ]\n[ ↑ 上へ ] [ ↓ 下へ ] [ 先頭へ ] [ 順番を指定 ] [ 削除 ]\n[ ◀ 前へ ] [ %d / %d ] [ 次へ ▶ ] [ ↻ 更新 ]\n[ 追加者を変更 ] [ 再生キューへ戻る ]", (start-1)/10+1, (total+9)/10)},
	}
	e.Footer["text"] = fmt.Sprintf("待機%d曲 · 表示時点の追加者内の順番\n表示確認用 · リンクはYouTubeトップ・操作不可", total)
	return e
}
