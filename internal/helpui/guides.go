// Package helpui renders static help without accessing playback or external sites.
package helpui

// Guide is editorial content. Command references in Body are replaced by the
// renderer, so translations need not know Discord application command IDs.
type Guide struct {
	ID, Name, Summary, Body string
}

func Guides() []Guide {
	return []Guide{
		{"guide:home", "基本の使い方", "再生を始める・操作を探す", "VCに参加して、{{play}} に動画や音楽のURLを指定すると再生できます。\n\n**曲を追加**\n- {{play}} キューに追加\n- {{playnext}} 自分の待機曲の先頭に追加\n\n**再生を操作**\n- {{skip}} 現在の曲をスキップ\n- {{stop}} 再生を停止して退出\n- {{volume}} 音量を表示・変更\n\n**曲を確認**\n- {{nowplaying}} 再生中の曲\n- {{queue}} 再生予定\n- {{history}} 履歴を表示・曲を再追加\n\n**関連曲を聴く**\n- {{mix}} Mixからまとめて追加\n- {{autoplay on}} 待機曲がなくなったら自動再生"},
		{"guide:add", "再生・追加", "単曲・プレイリスト・先頭追加", "**順番に聴きたいとき**\n- {{play}} に動画URLを指定すると、曲をキューに追加します。\n- YouTube・YouTube MusicのプレイリストURLなら、まとめて追加できます。\n\n**自分の待機曲より先に聴きたいとき**\n- {{playnext}} で自分の待機曲の先頭に追加します。\n- 他の人の順番は維持し、再生中の曲は中断しません。\n\n**動画に付いているリストも聴きたいとき**\n- リスト付き動画URLでは、まずその曲だけを追加します。\n- 追加結果のボタンから、付属プレイリストやMixも取り込めます。\n\n単曲はYouTube・YouTube Music・ニコニコ動画に対応しています。"},
		{"guide:control", "再生操作", "スキップ・停止・再開・音量", "**曲を飛ばす**\n- {{skip}} で次の曲へ進みます。\n\n**停止して、後で再開する**\n- {{stop}} で再生を停止し、VCから退出します。待機キューは保持します。\n- 再開するときはVCに参加し、URLなしで {{play}} を実行します。\n\n**会話に合わせて音量を調整する**\n- {{volume}} で音量を確認します。`percent` を指定すると変更できます。\n- 0はミュート、100は元音量です。初期設定はBGM向けの30%です。"},
		{"guide:queue", "キュー・履歴", "再生予定と過去の曲を確認する", "**これから聴く曲を確認**\n- {{queue}} で再生予定を表示します。前へ・次へ・更新のボタンで確認できます。\n- 自動再生がONで通常の待機曲がないときは、取得済みの候補を表示します。\n- `action:clear` は待機キューの全消去です。表示だけの操作とは異なります。\n\n**過去の曲をもう一度聴く**\n- VCに参加して {{history}} を実行します。参加中のVCの履歴を表示します。\n- 前へ・次へ、中央のページ番号から移動できます。\n- 曲を選択し、再追加の操作でキューへ戻せます。\n\n**今の曲を確認**\n- {{nowplaying}} で再生中の曲と音量を確認できます。"},
		{"guide:autoplay", "Mix・自動再生", "まとめて追加する・関連曲を聴き続ける", "**関連曲をまとめて追加する**\n- {{mix}} にYouTube・YouTube Musicの動画URLを指定します。Mix付きでないURLも使えます。\n- 指定件数を上限に追加し、その後の自動補充はしません。\n\n**待機曲がなくなった後も聴き続ける**\n- {{autoplay on}} で有効にします。通常の待機曲がなくなると関連曲を再生します。\n- 有効な間に最後に再生を開始した、手動追加のYouTube曲が起点になります。起点がなければ、その曲の再生を待ちます。\n\n**途中で曲を追加したとき**\n- 手動で追加した曲が優先されます。再生中の曲は中断せず、その後に再生します。\n\n**確認・無効化**\n- {{autoplay status}} で状態と起点を確認します。\n- {{autoplay off}} で自動補充を停止します。現在の曲は継続します。"},
	}
}
