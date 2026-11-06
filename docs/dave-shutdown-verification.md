# DAVE終了待ちの検証

## 対象と採用理由

音声接続改善計画の「キャンセル通知と資源解放を区別する」条件を確認したところ、使用中のdave-go v0.5.1は`Close`で`shutdownDone`を直ちに閉じていた。`WaitShutdown`はその通知を待つだけで、監視goroutineの終了は待たない。コメントに記載された保証と実装が一致していなかった。

上流の修正[a2b0c8c1b872](https://github.com/thomas-vilte/dave-go/commit/a2b0c8c1b87269f846e84db95fde2404326c310f)は、回復監視とcommit確認監視をWaitGroupで追跡する。監視の登録とCloseを同じロックで直列化し、キャンセル後の登録を禁止する。Closeは待機処理を起動して返り、WaitShutdownは登録済み監視の実終了を待つ。

調査時の上流HEADは`083b593411546e51e528fba5315acf40bf95a4e2`だったが、今回は終了待ち修正のコミットに固定した。v0.5.1との差分には、それ以前のフレーム境界検査、replay判定、protocol 0のReady判定、ログの秘密情報除去なども含まれる。終了待ちの4ファイルだけを取り込んだforkではない。MLS依存はv1.6.0のまま維持する。

## アプリ側の契約

失効通知では依存のCloseを呼び、送信コールバック内でWaitShutdownを呼ばない。Transportの後始末が送信・イベント処理を待った後に、guardedDave.Closeがプロトコルロック外でWaitShutdownを待つ。複数回のCloseも同じ終了を待つ。

この待機は資源枠の内側にある。5秒を超えた場合は遅延を記録するが、終了前に枠を再利用しない。制御側の期限付き待機と、後始末の実終了を区別する。

## 検証の証拠と限界

隔離した上流コミットで`go test ./session -run TestWaitShutdown -count=1 -timeout=60s`と`go test ./... -timeout=120s`が成功した。`TestWaitShutdown_WaitsForRunningWatchdog`は、回復監視とcommit確認監視のそれぞれを送信コールバック内で止め、終了待ちが期限切れになること、コールバックを解放すると完了することを確認する。`TestWaitShutdown_NoWatchdogAfterClose`はClose後に新たな監視を開始しないことを確認する。

依存更新後の`mise run check`（通常・本番タグのテスト、vet、Windows/Linuxビルド）とWSLの`mise run race-test`も成功した。これらは実Discordでの可聴性やVC移動の検証ではない。反映後の実機確認は未完了である。
