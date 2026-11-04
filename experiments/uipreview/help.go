package main

import "strings"

// Static layout previews; no commands or interactive components are registered.
func helpSample(variant string) embed {
	e := embed{Color: 0x527DA8, Footer: map[string]string{"text": "使い方の表示サンプル · コマンド実行・カテゴリ切替はできません"}}
	switch variant {
	case "help-ref-play":
		e.Title = "/play"
		e.Description = "曲・プレイリストをキューに追加、または停止後のキューを再開します。"
		e.Fields = []field{
			{Name: "書式", Value: "`/play [url:URL]`\n角括弧内は省略できます。"},
			{Name: "引数", Value: "**url** · 任意\nYouTube・YouTube Music・ニコニコ動画のURL。プレイリストはYouTube・YouTube Musicに対応。"},
			{Name: "動作", Value: "- 動画URL：その曲をキューに追加\n- プレイリストURL：複数曲をまとめて追加\n- URL省略：保持しているキューを再開\n- リスト付き動画URL：まず単曲を追加し、ボタンから付属リストも取り込み可能"},
			{Name: "使用例", Value: "`/play url:https://www.youtube.com/watch?v=TcLLpZBWsck`\n曲を追加\n\n`/play`\nキューを再開"},
			{Name: "利用条件", Value: "音声チャンネルへの参加が必要です。"},
		}
	case "help-ref-playnext":
		e.Title = "/playnext"
		e.Description = "曲を自分の待機曲の先頭に追加します。"
		e.Fields = []field{
			{Name: "書式", Value: "`/playnext url:URL`"},
			{Name: "引数", Value: "**url** · 必須\nYouTube・YouTube Music・ニコニコ動画の単曲URL。"},
			{Name: "動作", Value: "- 自分の次の番に再生する曲として追加\n- 他の人の順番は維持\n- 再生中の曲は中断しません\n- 付属リストは追加結果のボタンから取り込めます"},
			{Name: "使用例", Value: "`/playnext url:https://www.youtube.com/watch?v=TcLLpZBWsck`\n自分の待機曲より先に追加"},
			{Name: "利用条件", Value: "音声チャンネルへの参加が必要です。プレイリスト単独URLは指定できません。"},
		}
	case "help-ref-mix":
		e.Title = "/mix"
		e.Description = "動画を起点に、Mixから関連曲をまとめてキューに追加します。"
		e.Fields = []field{
			{Name: "書式", Value: "`/mix url:URL [count:件数]`\n角括弧内は省略できます。"},
			{Name: "引数", Value: "**url** · 必須\nYouTube・YouTube Musicの動画URL。\n\n**count** · 任意 · 1〜50 · 既定20\n起点の曲を含む追加上限。指定件数より少なくなる場合があります。"},
			{Name: "動作", Value: "- 動画にMixが付いていなくても指定可能\n- 取得した候補から指定件数を上限に追加\n- 追加後の自動補充はしません"},
			{Name: "使用例", Value: "`/mix url:https://www.youtube.com/watch?v=TcLLpZBWsck count:10`\n起点の曲を含めて最大10曲を追加"},
			{Name: "利用条件", Value: "音声チャンネルへの参加が必要です。"},
		}
	case "help-detail-heading":
		e.Title = "詳細D：コマンド名を見出しに"
		e.Description = "VCに参加して使います。YouTube・YouTube Music・ニコニコ動画に対応しています。\n\n" +
			"**/play**\n曲の追加・キューの再開\n- `url` に動画URLを指定すると、曲をキューに追加\n- YouTube・YouTube MusicのプレイリストURLなら、まとめて追加\n- URLなしで実行すると、保持しているキューを再開\n\n" +
			"**/playnext**\n自分の待機曲の先頭に追加\n- `url` に動画URLを指定\n- 他の人の順番は維持し、再生中の曲は中断しません\n\n" +
			"**付属リストの取り込み**\nリスト付き動画URLでは、まずその曲だけを追加します。追加結果のボタンから、付属プレイリストやMixも取り込めます。"
	case "help-detail-hybrid":
		e.Title = "詳細E：コマンド名＋目的を見出しに"
		e.Description = "VCに参加して使います。YouTube・YouTube Music・ニコニコ動画に対応しています。\n\n" +
			"**/play — 曲の追加・再開**\n- **動画URLあり**：曲をキューに追加\n- **プレイリストURLあり**：まとめて追加（YouTube・YouTube Music）\n- **URLなし**：保持しているキューを再開\n\n" +
			"**/playnext — 自分の待機曲の先頭に追加**\n`url` に動画URLを指定します。他の人の順番は維持し、再生中の曲は中断しません。\n\n" +
			"**付属リストも追加したいとき**\nリスト付き動画URLでは、まずその曲だけを追加します。追加結果のボタンから、付属プレイリストやMixを取り込めます。"
	case "help-detail-reference":
		e.Title = "詳細F：入力例ごとの早見表"
		e.Description = "VCに参加して使います。YouTube・YouTube Music・ニコニコ動画に対応しています。\n\n" +
			"**/play**\n`/play url:動画URL`\n曲をキューに追加\n\n`/play url:プレイリストURL`\n複数曲をまとめて追加（YouTube・YouTube Music）\n\n`/play`\n保持しているキューを再開\n\n" +
			"**/playnext**\n`/playnext url:動画URL`\n自分の待機曲の先頭に追加。他の人の順番は維持し、再生中の曲は中断しません。\n\n" +
			"**リスト付き動画URLの場合**\nまず単曲を追加 → 追加結果のボタンから付属プレイリストやMixを取り込み"
	case "help-detail-bullets":
		e.Title = "詳細A：目的別の箇条書き"
		e.Description = "YouTube・YouTube Music・ニコニコ動画のURLに対応しています。\n\n" +
			"**曲をキューに追加**\n- `/play url:動画URL` で単曲を追加\n- YouTube・YouTube MusicのプレイリストURLなら、複数曲をまとめて追加\n\n" +
			"**自分の待機曲の先頭に追加**\n- `/playnext url:動画URL` を使用\n- 他の人の順番は維持し、再生中の曲は中断しません\n\n" +
			"**動画に付属するリストも追加**\n- リスト付き動画URLでは、まずその曲だけを追加\n- 追加結果のボタンから、付属プレイリストやMixを取り込めます\n\n" +
			"**停止後に再開**\n- VCに参加して、URLなしで `/play` を実行\n- 保持しているキューを再開します"
	case "help-detail-commands":
		e.Title = "詳細B：コマンドと補足を分ける"
		e.Description = "YouTube・YouTube Music・ニコニコ動画のURLに対応しています。\n\n" +
			"**曲をキューに追加**\n`/play url:動画URL`\n単曲を追加します。YouTube・YouTube MusicのプレイリストURLなら、複数曲をまとめて追加できます。\n\n" +
			"**自分の待機曲の先頭に追加**\n`/playnext url:動画URL`\n他の人の順番は維持し、再生中の曲は中断しません。\n\n" +
			"**動画に付属するリストも追加**\n追加結果のリスト取り込みボタン\nリスト付き動画URLでは、まずその曲だけを追加します。ボタンから付属プレイリストやMixを取り込めます。\n\n" +
			"**停止後に再開**\n`/play`（URLなし）\nVCに参加して実行すると、保持しているキューを再開します。"
	case "help-detail-prose":
		e.Title = "詳細C：短い説明文でまとめる"
		e.Description = "YouTube・YouTube Music・ニコニコ動画のURLに対応しています。\n\n" +
			"**曲をキューに追加**\n`/play url:動画URL` で単曲を追加します。YouTube・YouTube MusicのプレイリストURLなら、複数曲をまとめて追加できます。\n\n" +
			"**自分の待機曲の先頭に追加**\n`/playnext url:動画URL` を使います。他の人の順番は維持し、再生中の曲は中断しません。\n\n" +
			"**動画に付属するリストも追加**\nリスト付き動画URLでは、まずその曲だけを追加します。追加結果のボタンから、付属プレイリストやMixを取り込めます。\n\n" +
			"**停止後に再開**\nVCに参加し、URLなしで `/play` を実行すると、保持しているキューを再開します。"
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
