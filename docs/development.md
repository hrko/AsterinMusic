# 開発・運用手順

曲の切り替わりで再起動する場合は`mise run bot-restart -- --after-track`を使う。ビルドと事前検証を先に完了し、その時点の曲から次の曲の最初の音声パケットが送信されるまで待機する。管理状態を100ms間隔で確認するため厳密な同時実行ではなく、検知直後に停止・再接続する。次曲はチェックポイントから先頭へ戻って再開する。待機中に再生が停止・終了した場合、または最初から再生していない場合はその時点で再起動する。

待機中はタスクを実行した端末を維持する。Ctrl+Cで待機を取り消すと旧Botは継続する。`bot-status`と`bot-logs`は待機中も実行でき、他の起動・停止操作は排他ガードで拒否する。状態取得失敗・プロセス変更時は再起動しない。この機能に未対応の旧Botは通常再起動で一度更新する必要がある。

キュー表示を確認する際はDiscordで`/queue`を実行し、本人専用表示・メンション・前後ページ・更新を確認する。自動再生ONで最後の手動曲が再生開始した後、更新すると先行取得された候補一覧を表示する。手動追加後の更新では手動キューの1ページ目へ戻る。コマンド説明を変更した場合は`mise run register`で反映する。

ニコニコ認証の設定・ChromeでのCookie保存は[ニコニコ動画の認証](niconico-auth.md)を参照。`mise run source-check -- URL`はYAMLの認証設定と選択済みyt-dlpで単曲のメタデータ・音声取得を検証する。Discordへ接続せず、音声は最大256MiB・5分で取得・破棄する。実際の認証情報はテストに埋め込まない。

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

`bot-restart`は再生中なら自動再接続して曲の先頭から再開し、停止中なら停止を維持します。音量・自動再生設定も保存します。`bot-stop`後の`bot-start`、旧版からの初回移行、接続失敗時はURLなしの`/play`で再開してください。ビルドまたは状態保存に失敗した場合は旧版を停止しません。正常終了が45秒以内に完了しない場合は、強制終了や追加起動をせずエラーにします。起動確認が60秒でタイムアウトした場合も、再実行前に`bot-status`と`bot-logs`で確認してください。

管理処理はOSのプロセス一覧と、プロジェクト単位の認証付きローカル制御接続を照合します。調査できないプロセス・管理外のBot・複数Botを検出した場合は操作を拒否します。制限された実行環境での空の検索結果を根拠に起動しないでください。ローカル制御用ポートが競合した場合も起動を拒否します。制御情報は`.dev/run`、ログは`.dev/logs`に保存します。実行ファイルは起動ごとに`bin/asterinmusic-voice-<識別子>`を作成します。

`voice-check`も同じ操作ロックを使い、Botの稼働中は拒否します。実チャンネルへのテスト音送信は、必要なときに`bot-stop`で停止してから行ってください。

名称変更前の`rainmusic-voice-*`もプロセス検出の対象に残しているため、旧版の稼働中でも管理タスクで移行できる。設定ファイルを改名した直後は`mise run bot-restart -- --after-track --config asterinmusic.yaml`で新しいパスを明示する。作業フォルダー名は任意であり、Botの稼働中は変更しない。プロセス照合と制御ポートの算出に絶対パスを使用するため、フォルダーを移動する場合は先に管理タスクで停止する。

この方式は同じプロジェクト内の開発用です。別のチェックアウトから同じBotトークンを使って並行起動しないでください。今後のエージェント向け操作規則は[AGENTS.md](../AGENTS.md)に記載しています。

## 検証・診断タスク

| タスク | 用途 |
| --- | --- |
| `mise run fmt` | Goソースを整形 |
| `mise run title-plugin-probe` | 実験用yt-dlpプラグインの言語別タイトル取得を検証。Botへの組み込みなし。[検証結果](title-plugin-probe.md) |
| `mise run youtube-data-probe` | `.env.youtube` の `YOUTUBE_DATA_API_KEY` で5動画のタイトルをja/en/言語未指定で比較し、本番の補完器でも3曲の日本語タイトルを検証。Discord接続なし。[検証結果](title-plugin-probe.md) |
| `mise run check` | 整形、通常構成と本番構成（`davego,opus`・SIMD・CGO無効）のvet・単体テスト、両構成のLinux/Windowsクロスビルド |
| `mise run race-test` | 並行処理の競合検出 |
| `mise run audio-check` | FFmpeg変換、音量、ミュート、キャンセル |
| `mise run opus-audio-check` | ライブ用gopus SIMD経路、音量反映時間、送信間隔、キャンセル（CGO無効） |
| `mise run dave-check` | Pure Go DAVE組み込み・上流DAVEテスト（CGO無効） |
| `mise run dave-interop` | 独立モジュール`experiments/daveinterop`でGo MLSと公式libdaveの相互運用を検証（CGOあり）。初回は`mise -C experiments install`で任意依存を準備 |
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

保存先は既定で`.dev/yt-dlp`です。運用環境では`ASTERINMUSIC_UPDATE_DIR`で永続ディレクトリを指定してください。更新中に異常終了して`update.lock`が残った場合は、更新プロセスが動いていないことを確認してから削除します。miseのロックやツール本体は更新機構から変更しません。

## WebhookによるUI検証

外部モデルによるコード点検は [Agyレビュー手順](agy-review.md) を参照する。Botの実行管理とは別の読み取りレビューとして扱う。

`mise run ui-preview -- brackets` はリンク名の角括弧を、バックスラッシュ・そのまま・HTML文字参照・全角置換で比較する。入れ子と片側だけの括弧も含め、文字表示とリンク範囲を実画面で確認する。

Botを再起動せず、mise run ui-preview -- a b c でレイアウトを指定の検証用Webhookへ送信する。aは番号の通常テキスト化、bは補足の字下げ、cは曲別フィールド。mise run ui-preview -- d e f は自動番号のまま、行末スペース2個・バックスラッシュ・subtext記法を比較する。引数省略時はa/b/cの3件を送る。

Git管理外の .env.ui-preview に DISCORD_UI_PREVIEW_WEBHOOK を設定する。URL・応答本文・HTTPエラー原文は出力しない。実行には投稿先への送信許可が必要。サンプルは再生やキュー操作を行わず、通知メンションも抑止する。HTTP成功は投稿成功だけを意味し、レイアウトの採否はDiscord実画面で確認する。

開始番号の検証には mise run ui-preview -- d11 を使う（D方式・11〜20番）。mise run ui-preview -- df11 は同じ開始番号で、行末スペース2個の改行と補足のsubtextを組み合わせる。

環境変数 `UI_PREVIEW_USER_ID` に検証対象のユーザーIDを指定すると、サンプルユーザーの表記を実際のメンションに置き換える。通知は引き続き抑止する。未指定時はプレーンテキストで表示する。

`mise run ui-preview -- end0 end1 end2` はD＋Fの本文末尾の改行を0個・1個・2個で比較する。本番と同じく自動再生の説明を別フィールドに置き、一覧の最終行でsubtextが成立するかを確認する。

`mise run ui-preview -- digits1 digits11 digits1fixed` は同じ10曲で、1〜10番・11〜20番・1〜10番の補足インデント3スペース固定を比較する。前2つは各番号の幅に補足インデントを合わせる。本文末尾の改行なし、自動再生説明は別フィールドで統一する。

`mise run ui-preview -- digits91fixed digits91` は91〜100番で、補足インデント4スペース固定と、100番だけ5スペースに増やす方式を比較する。

`mise run ui-preview -- image-none image-split image-bottom image-icon` は同じ曲一覧で、画像なし・再生中と一覧のEmbed分離・下部画像・見出しアイコンを比較する。スマホでの本文幅と縦の長さを確認する。補足のインデントはページ内で固定する。

`mise run ui-preview -- width-current width-standard width-compact` は画像なしで、現行の文字数制限・Unicode幅56/24・幅40/18（曲名/チャンネル名）を比較する。幅計算にはdisplaywidthを使い、曖昧幅は1に固定し、書記素の境界で省略する。幅はDiscordのピクセル幅ではなく等幅表示に基づく目安。架空の多言語・絵文字タイトルを含み、リンクはYouTubeトップへの表示検証用リンク。
