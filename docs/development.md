# 開発・運用手順

接続・動作設定は[設定ファイル](configuration.md)に記述します。Botプロフィールの曲名表示は既定で有効です。無効にする場合はYAMLの`bots.<name>.activity.enabled`を`false`にして、`mise run bot-restart`で反映します。追加権限やPresence Intentの有効化は不要です。設定外のサーバーへの参加を検出した場合は、表示設定に関係なくアクティビティを消去します。

セットアップは[README](../README.md)、利用者向けの動作は[現行仕様](specification.md)、エージェントの操作規則は[AGENTS.md](../AGENTS.md)を参照。

## Botの起動管理

開発中のBot操作は以下のタスクに統一します。Windows/LinuxともGo製の管理処理を使い、起動はバックグラウンドで行います。`serve`も`bot-start`と同じ動作です。

| タスク | 動作 |
| --- | --- |
| `mise run bot-start` | 停止中ならビルドして起動。起動済みなら追加起動しない。Discord接続完了を待つ |
| `mise run bot-stop` | Bot自身へ正常終了を依頼し、プロセス終了を確認 |
| `mise run bot-restart` | 新版を別ファイルへビルドしてから旧版を正常終了し、新版の接続完了を確認 |
| `mise run bot-status` | PID・実行ファイル・Discord接続準備完了状態を確認 |
| `mise run bot-logs` | 最新の起動ログの末尾を表示 |
| `mise run bot-restart-go` | Pure Go DAVE版へ切り替えて再起動（既定） |
| `mise run bot-restart-native` | 従来のlibdave版へ切り戻して再起動 |

`bot-restart`は再生中なら自動再接続して曲の先頭から再開し、停止中なら停止を維持します。音量・自動再生設定も保存します。`bot-stop`後の`bot-start`、旧版からの初回移行、接続失敗時はURLなしの`/play`で再開してください。ビルドまたは状態保存に失敗した場合は旧版を停止しません。正常終了が45秒以内に完了しない場合は、強制終了や追加起動をせずエラーにします。起動確認が60秒でタイムアウトした場合も、再実行前に`bot-status`と`bot-logs`で確認してください。

管理処理はOSのプロセス一覧と、プロジェクト単位の認証付きローカル制御接続を照合します。調査できないプロセス・管理外のBot・複数Botを検出した場合は操作を拒否します。制限された実行環境での空の検索結果を根拠に起動しないでください。ローカル制御用ポートが競合した場合も起動を拒否します。制御情報は`.dev/run`、ログは`.dev/logs`に保存します。実行ファイルは起動ごとに`bin/rainmusic-voice-<識別子>`を作成します。

`voice-check`も同じ操作ロックを使い、Botの稼働中は拒否します。実チャンネルへのテスト音送信は、必要なときに`bot-stop`で停止してから行ってください。

この方式は同じプロジェクト内の開発用です。別のチェックアウトから同じBotトークンを使って並行起動しないでください。今後のエージェント向け操作規則は[AGENTS.md](../AGENTS.md)に記載しています。

## 検証・診断タスク

| タスク | 用途 |
| --- | --- |
| `mise run fmt` | Goソースを整形 |
| `mise run check` | 整形、vet、単体テスト、CGO無効のLinux/Windowsクロスビルド |
| `mise run race-test` | 並行処理の競合検出 |
| `mise run audio-check` | FFmpeg変換、音量、ミュート、キャンセル |
| `mise run native-audio-check` | ライブ用gopus SIMD経路、音量反映時間、送信間隔、キャンセル（CGO無効） |
| `mise run dave-check` | Pure Go DAVE組み込み・上流DAVEテスト（CGO無効） |
| `mise run dave-interop` | Go MLSと公式libdaveのWelcome・双方向暗号フレーム相互運用（CGOあり） |
| `mise run build-voice-native` | 切り戻し用libdave版をビルド |
| `mise run opus-compare` | 独立モジュールでPure Go Opusとlibopusを比較（[結果](opus-comparison.md)） |
| `mise run opus-pure-check` | CGO無効でPure Go候補を実行 |
| `mise run opus-simd-compare` | Go 1.27・amd64 v3でgopus通常版／SIMD版を3回ずつ比較（[結果](opus-simd-comparison.md)） |
| `mise run quality-prepare` | 26音源のViSQOL用クリップとエンコード出力を作成（[準備手順・結果](visqol-comparison.md)） |
| `mise run quality-score` | ViSQOLで650組を採点（WSL/Linuxで検証） |
| `mise run quality-summary` | ViSQOLの音源別・音量別・チャンネル別集計 |
| `mise run build` | 診断CLIのビルド |
| `mise run build-voice` | 現在のOS用のDAVE対応ビルド |
| `mise run voice-check` | 設定した音声チャンネルで3秒の低音量テスト音を送信 |
| `mise run inspect -- URL` | メタデータをJSONで出力 |
| `mise run inspect-mix -- URL` | Mixの取得を検証（最大100候補、キュー変更なし） |
| `mise run capture -- URL OUTPUT` | 音声取得を最大10秒・32MiBで検証 |
| `mise run render -- INPUT OUTPUT.ogg` | ローカル音声を初期音量のOgg Opusに変換 |
| `mise run stream-check -- URL` | Discordに送信せず、実時間で曲の最後まで処理 |

`capture`の10秒は取得処理の実行時間です。打ち切ったファイルは不完全な場合があります。`render`は既存ファイルを上書きしません。[検証記録](verification.md)に確認範囲と制限を記載しています。

## yt-dlpの更新

Botは起動時と24時間ごとに公式の最新リリースを確認し、ログに記録します。適用は運用者がCLIから明示的に行います。

```text
mise run update-check
mise run update-apply -- VERSION SAMPLE_URL
mise run update-rollback
```

`update-apply`は公式バイナリのSHA256、実行時のバージョン、指定した公開URLのメタデータ取得を検証してから次回起動用のバージョンを選択します。失敗した候補は有効化しません。`update-rollback`で直前の選択に戻せます。最初の更新から戻す場合はmise管理のバイナリに戻ります。どちらもBotの再起動で反映します。適用前には対象サイトの音声取得も別途確認してください。

保存先は既定で`.dev/yt-dlp`です。運用環境では`RAINMUSIC_UPDATE_DIR`で永続ディレクトリを指定してください。更新中に異常終了して`update.lock`が残った場合は、更新プロセスが動いていないことを確認してから削除します。miseのロックやツール本体は更新機構から変更しません。
