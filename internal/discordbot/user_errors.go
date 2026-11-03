package discordbot

// Codes classify application-owned, safe-to-display failures independently
// from their wording. Never convert arbitrary upstream text to this type.
type operationError uint8

const (
	errJoinVoice operationError = iota + 1
	errSameVoice
	errRestarting
	errQueueChanged
	errVoiceChanged
	errStopping
	errQueueFull
	errQueueEmpty
	errNotPlaying
	errVoiceConnection
	errQueueSave
	errConnecting
)

func (e operationError) Error() string {
	switch e {
	case errConnecting:
		return "音声接続中です。少し待って再実行してください。"
	case errJoinVoice:
		return "音声チャンネルへ参加してください。"
	case errSameVoice:
		return "Botと同じ音声チャンネルで操作してください。"
	case errRestarting:
		return "再起動処理中です。"
	case errQueueChanged:
		return "取得中に停止・キュー消去が行われました。もう一度追加してください。"
	case errVoiceChanged:
		return "音声チャンネルが変更されました。"
	case errStopping:
		return "停止処理中です。少し待って再実行してください。"
	case errQueueFull:
		return "キューが満杯です。"
	case errQueueEmpty:
		return "キューは空です。"
	case errNotPlaying:
		return "再生中の曲はありません。"
	case errVoiceConnection:
		return "音声接続に失敗しました。追加した曲はキューに保持しています。"
	case errQueueSave:
		return "キューはメモリ上に保持していますが、保存に失敗しました。再起動前にログと保存先を確認してください。"
	default:
		return "処理を完了できませんでした。しばらく待って再実行してください。"
	}
}
