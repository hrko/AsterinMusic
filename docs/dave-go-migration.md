# Pure Go DAVEへの移行

> 検証・移行時点の記録です。現在の動作は[現行仕様](specification.md)、構成は[アーキテクチャ](architecture.md)、操作は[開発・運用手順](development.md)を参照してください。

2026-11-02。既定のDAVE実装を`github.com/thomas-vilte/dave-go v0.5.1`へ変更した。コミットは`7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5`、MLS依存は`mls-go v1.6.0`。go.mod/go.sumで固定している。これに伴いx/cryptoはv0.54.0、x/sysはv0.47.0になった。音声エンコードは既存gopus SIMDのまま。

標準の音声Botは`CGO_ENABLED=0`でビルドする。Windows/Linuxとも`davego,opus`、GOEXPERIMENT=simd、GOAMD64=v3を使用。起動にはFFmpeg・yt-dlpなどの外部コマンドが引き続き必要で、音声デコードのPure Go化は今回の対象外。

## 組み込み

godave v0.3.0のインターフェースを通じてdisgo v0.19.6へ接続した。上流コードの変更はしていない。音声送信では次の補助処理を追加した。

- DAVEが有効な鍵交換中は音声を送信せず、同じOpusパケットを最大15秒待機する。キャンセルに応答し、再生位置は送信成功時だけ進める。
- DAVEイベント処理と送信前の確認を直列化する。上流の回復処理が並行してepochを消す場合も、暗号化前の入力と同一の出力を検出してUDPへ渡さない。
- 明示的にprotocol version 0が選択された場合は、Discordのトランスポート暗号化のみの送信を許可する。
- disgo v0.19.6は音声接続の破棄時にgodave.Session.Closeを呼ばないため、RainMusicで接続に対応するDAVEセッションを登録し、停止・接続失敗・チェック終了・アプリ終了時にCloseする。回復監視処理を残さないための対応。
- 開発用停止タスクでは、Windowsの終了途中のプロセス情報取得が一時的にAccess deniedになる場合に再確認する。エラーを「プロセス不在」と解釈せず、停止確認が取れるまで後続起動しない。

## 検証

- WindowsのCGO無効構成で本体の全テストとdave-goのcodecs/frame/mediakeys/sessionテストが成功。
- WSL Linuxの競合検出付き本体・dave-goテストが成功。
- Windows/LinuxのCGO無効クロスビルドが成功。
- 公式libdave 1.1.0-cppとの相互運用テストが成功。Go MLS peerが生成したWelcomeをlibdaveが受理し、同じMLS epochから鍵を導出して双方向各1,000個の合成Opusフレームを暗号化・復号。復号結果の完全一致と改変フレームの拒否を確認した。これはcodecデコード品質の試験ではなく、DAVEのフレーム・鍵導出互換性の試験。
- 相互運用テストの初回は、MLSMessage全体を生のWelcomeを要求するC APIへ渡したため失敗。テスト側でMLSMessageからWelcomeを取り出す修正後に成功。暗号実装の変更は不要だった。
- 鍵交換中の送信保留、キャンセル、protocol 0、同一パケットの再試行、切断時の後始末をテスト。
- Windowsの実Discord接続で、Pure Go DAVE版がMLS proposalを処理し、3秒・151フレームのテスト音を送信。ペーシング遅延は0件。
- 利用者が実際の曲の再生と、音声チャンネル退出・再参加後も音が聞こえることを確認。ログにもMLSグループの再生成・commit・joinを確認した。

音声確認はWindowsで実施。Linuxからの実Discord接続、多人数での連続入退室、長時間運転、ネットワーク障害中の復旧は未検証。相互運用テストは単一epochを使うため、鍵更新の網羅的な公式実装比較ではない。上流は第三者の正式なセキュリティ監査を受けていない旨を明記しており、今回の動作確認は監査の代替ではない。

## 操作

通常は`mise run bot-restart`、Pure Go版を明示する場合は`mise run bot-restart-go`。問題が出た場合は`mise run bot-restart-native`でlibdave版へ戻す。いずれも既存のプロセス確認・排他・キュー保持を通る。再起動後の再生再開はURLなしの`/play`。

検証を繰り返す場合は`mise run dave-check`と`mise run dave-interop`を使用する。後者はmise管理のlibdave・Zigを使う。切り戻し版のビルドだけなら`mise run build-voice-native`。稼働中Botへテスト音を重ねないため、`voice-check`は先に`bot-stop`が必要。

参照：[dave-go](https://github.com/thomas-vilte/dave-go)、[公式libdave](https://github.com/discord/libdave)。
