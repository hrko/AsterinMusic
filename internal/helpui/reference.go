package helpui

// Reference contains the prose which cannot be derived from command options.
type Reference struct {
	Behavior, Conditions string
	Examples             []Example
	Defaults             map[string]string
}
type Example struct{ Purpose, Command string }

func references() map[string]Reference {
	const video = "https://www.youtube.com/watch?v=TcLLpZBWsck"
	const voice = "音声チャンネルへの参加が必要です。Botが別のVCで使用中の場合は、同じVCへ参加してください。"
	return map[string]Reference{
		"play":       {Behavior: "- 動画URL：その曲をキューに追加\n- プレイリストURL：複数曲を追加（YouTube・YouTube Music）\n- URL省略：保持しているキューを再開\n- リスト付き動画URL：まず単曲を追加し、ボタンから付属リストも取り込み可能", Conditions: voice, Examples: []Example{{"曲を追加", "/play url:" + video}, {"キューを再開", "/play"}}},
		"playnext":   {Behavior: "- 自分の待機曲の先頭に追加\n- 他の人の順番を維持し、再生中の曲は中断しません\n- 付属リストは追加結果のボタンから取り込めます", Conditions: voice + "プレイリスト単独URLは指定できません。", Examples: []Example{{"自分の待機曲より先に追加", "/playnext url:" + video}}},
		"mix":        {Behavior: "- Mixが付いていない動画URLも指定可能\n- 起点の曲を含めて指定件数を上限に追加\n- 候補によって指定件数より少なくなる場合があります\n- 追加後の自動補充はしません", Conditions: voice, Defaults: map[string]string{"count": "20"}, Examples: []Example{{"最大10曲を追加", "/mix url:" + video + " count:10"}}},
		"stop":       {Behavior: "- 再生中の曲を終了してVCから退出\n- 待機キューは保持\n- 自動再生はOFFになります\n- URLなしの /play で待機キューを再開できます", Conditions: voice, Examples: []Example{{"停止して退出", "/stop"}}},
		"skip":       {Behavior: "- 再生中の曲を終了し、次の曲へ進みます", Conditions: voice, Examples: []Example{{"現在の曲をスキップ", "/skip"}}},
		"queue":      {Behavior: "- 表示は本人限定。前へ・次へ・更新ボタンで操作\n- キューを編集：待機曲を選び、追加者内で移動・削除。編集結果も本人限定\n- 先頭への移動は、その追加者の先頭です。全体の次曲になるとは限りません\n- 通常の待機曲がなく、自動再生がONなら取得済みの候補を表示\n- edit userで追加者を指定可能。省略時は自分を優先\n- clearは本人限定の確認後に手動の待機曲を全消去。再生中の曲と自動再生設定は維持", Conditions: "表示にはVC参加は不要です。編集・全消去は対象VCへの参加と操作権限が必要です。編集画面は15分間有効です。", Defaults: map[string]string{"page": "1"}, Examples: []Example{{"再生予定を確認・編集", "/queue show"}, {"編集画面を直接開く", "/queue edit"}, {"待機曲を全消去", "/queue clear"}}},
		"nowplaying": {Behavior: "- 再生中の曲・追加者・音量・表示時点の経過時間を本人限定で表示\n- 定期的な表示更新はしません", Conditions: "VC参加は不要です。", Examples: []Example{{"今の曲を確認", "/nowplaying"}}},
		"volume":     {Behavior: "- percent省略：現在の音量を本人限定で表示\n- percent指定：再生音量を変更\n- 0はミュート、100は元音量。初期設定は30%", Conditions: "確認にはVC参加は不要です。変更はBotと同じVCへの参加が必要です。", Examples: []Example{{"音量を確認", "/volume"}, {"30%に変更", "/volume percent:30"}}},
		"autoplay":   {Behavior: "- on：通常の待機曲がなくなったら関連曲を自動再生\n- 起点は有効な間に最後に再生を開始した手動追加のYouTube曲\n- 手動追加曲を優先し、現在の曲は中断しません\n- off：自動補充を停止。現在の曲は継続\n- status：状態と起点を本人限定で表示", Conditions: "状態確認にはVC参加は不要です。ON／OFFの変更にはVCへの参加が必要です。", Examples: []Example{{"自動再生を有効化", "/autoplay on"}, {"状態を確認", "/autoplay status"}}},
		"history":    {Behavior: "- 参加中のVCの履歴を本人限定で表示\n- 前へ・次へ、中央のページ番号で移動。更新で最新ページへ戻ります\n- 表示中の曲を選び、通常追加または自分の待機曲の先頭に再追加\n- 操作した人を追加者とし、付属リストは取り込みません", Conditions: "閲覧対象のVCへの参加と閲覧・接続権限が必要です。操作期限は15分です。", Examples: []Example{{"参加中VCの履歴を確認", "/history"}}},
		"help":       {Behavior: "- page省略：基本の使い方を表示\n- page指定：ガイドまたはコマンド詳細を直接表示\n- 二つのメニューから同じ画面内で移動\n- 無効な指定は基本画面と選び直しの案内を表示", Conditions: "VC参加は不要です。本人限定で表示し、操作期限は15分です。", Defaults: map[string]string{"page": "基本の使い方"}, Examples: []Example{{"基本画面を開く", "/help"}, {"/playの詳細を開く（pageの候補から選択）", "/help page:command:play"}}},
	}
}
