package main

import "strings"

// Static layout previews; no commands or interactive components are registered.
func helpSample(variant string) embed {
	e := embed{Color: 0x527DA8, Footer: map[string]string{"text": "使い方の表示サンプル · コマンド実行・カテゴリ切替はできません"}}
	switch variant {
	case "help-bullets":
		e = helpSample("help-home")
		e.Title = "A：カテゴリ別の箇条書き"
		for _, f := range e.Fields {
			e.Description += "\n\n**" + f.Name + "**\n- " + strings.ReplaceAll(f.Value, "\n", "\n- ")
		}
		e.Fields = nil
	case "help-compact":
		e.Title = "B：目的とコマンドのコンパクトな一覧"
		e.Description = "VCに参加して、`/play` に動画や音楽のURLを指定すると再生できます。\n\n" +
			"**曲を追加**\nキューに追加 → `/play`\n自分の待機曲の先頭に追加 → `/playnext`\n\n" +
			"**再生を操作**\nスキップ → `/skip`\n停止して退出 → `/stop`\n音量を表示・変更 → `/volume`\n\n" +
			"**曲を確認**\n再生中 → `/nowplaying`\n再生予定 → `/queue`\n履歴・再追加 → `/history`\n\n" +
			"**関連曲を聴く**\nMixからまとめて追加 → `/mix`\n待機曲の後に自動再生 → `/autoplay on`"
	case "help-steps":
		e.Title = "C：最初の手順＋操作一覧"
		e.Description = "**まずは再生してみる**\n1. 音声チャンネルに参加\n2. `/play` の `url` に動画や音楽のURLを指定\n3. 別の曲も `/play` でキューに追加\n\n" +
			"**追加・再生操作**\n- `/playnext` 自分の待機曲の先頭に追加\n- `/skip` 現在の曲をスキップ\n- `/stop` 再生を停止して退出\n- `/volume` 音量を表示・変更\n\n" +
			"**曲を確認**\n- `/nowplaying` 再生中の曲\n- `/queue` 再生予定\n- `/history` 履歴を表示・曲を再追加\n\n" +
			"**関連曲を聴く**\n- `/mix` Mixからまとめて追加\n- `/autoplay on` 待機曲がなくなったら自動再生"
	case "help-home":
		e.Title = "AsterinMusicの使い方"
		e.Description = "VCに参加して、`/play` に動画や音楽のURLを指定すると再生できます。"
		e.Fields = []field{
			{Name: "曲を追加", Value: "`/play` キューに追加\n`/playnext` 自分の待機曲の先頭に追加"},
			{Name: "再生を操作", Value: "`/skip` 現在の曲をスキップ\n`/stop` 再生を停止して退出\n`/volume` 音量を表示・変更"},
			{Name: "曲を確認", Value: "`/nowplaying` 再生中の曲\n`/queue` 再生予定\n`/history` 履歴を表示・曲を再追加"},
			{Name: "関連曲を聴く", Value: "`/mix` Mixからまとめて追加\n`/autoplay on` 待機曲がなくなったら自動再生"},
		}
	case "help-add":
		e.Title = "曲を追加する"
		e.Description = "YouTube・YouTube Music・ニコニコ動画のURLに対応しています。"
		e.Fields = []field{
			{Name: "通常の追加", Value: "`/play url:動画URL` で曲をキューに追加します。\nYouTube・YouTube MusicのプレイリストURLなら、複数曲をまとめて追加できます。"},
			{Name: "先に聴きたい曲", Value: "`/playnext url:動画URL` で自分の待機曲の先頭に追加します。他の人の順番は維持し、再生中の曲は中断しません。"},
			{Name: "動画に付属するリスト", Value: "リスト付きの動画URLでは、まずその曲を追加します。追加結果のボタンから、付属プレイリストやMixも取り込めます。"},
			{Name: "停止後に再開", Value: "VCに参加し、URLを指定せず `/play` を実行すると、保持しているキューを再開できます。"},
		}
	case "help-autoplay":
		e.Title = "Mix・自動再生"
		e.Description = "まとめて曲を追加するにはMix、待機曲がなくなった後も聴き続けるには自動再生を使います。"
		e.Fields = []field{
			{Name: "Mixから追加", Value: "`/mix url:動画URL` で、YouTube・YouTube Musicの動画を起点に関連曲をまとめて追加します。\n追加する件数は `count` で指定できます。追加後の自動補充はしません。"},
			{Name: "自動再生を有効にする", Value: "`/autoplay on` で有効にします。手動追加の待機曲がなくなると、関連曲を再生します。\n有効な間に最後に再生を開始した、手動追加のYouTube曲が起点になります。"},
			{Name: "自分で追加した曲を聴く", Value: "手動で追加した曲が優先されます。再生中の曲は中断せず、その後に再生します。"},
			{Name: "確認・無効化", Value: "`/autoplay status` 状態と起点を確認\n`/autoplay off` 自動補充を停止（現在の曲は継続）"},
		}
	}
	return e
}
