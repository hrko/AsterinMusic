package main

import "strings"

func bracketSample() embed {
	e := embed{Title: "曲名リンク内の角括弧比較", Color: 0x527DA8, Footer: map[string]string{"text": "表示検証用 · リンクはYouTubeトップ · 曲の追加・再生なし"}}
	titles := []string{"藤井 風 - 青春病 (Live at Nissan Stadium) [Day2]", "[Live] 曲名 [2026]", "曲名 [外側[内側]]", "閉じ括弧だけ ] の曲名", "開き括弧だけ [ の曲名"}
	variants := []struct {
		name    string
		convert func(string) string
	}{
		{"A：現在の方式（バックスラッシュ）", func(s string) string { return strings.NewReplacer("[", "\\[", "]", "\\]").Replace(s) }},
		{"B：角括弧をそのまま使用", func(s string) string { return s }},
		{"C：HTML文字参照", func(s string) string { return strings.NewReplacer("[", "&#91;", "]", "&#93;").Replace(s) }},
		{"D：全角括弧に置換（表示変更あり）", func(s string) string { return strings.NewReplacer("[", "［", "]", "］").Replace(s) }},
	}
	for _, v := range variants {
		e.Description += "**" + v.name + "**\n"
		for _, title := range titles {
			e.Description += "**[" + v.convert(title) + "](https://www.youtube.com/)**\n"
		}
		e.Description += "\n"
	}
	return e
}
