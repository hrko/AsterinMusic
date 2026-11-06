# AsterinMusic

Go製のDiscord音楽Bot。YouTube / YouTube Music / ニコニコ動画の公開音声を再生します。追加者ごとの公平なキュー、プレイリスト・Mixの取り込み、自動再生、BGM向けの音量調整を備えています。

Bot本体はgopus SIMDとdave-goを使い、既定で`CGO_ENABLED=0`でビルドします。音源取得・デコードにはyt-dlp、Deno、FFmpegを使用します。

## 文書

- [現行仕様・要件](docs/specification.md)：対応範囲、コマンド、公平キュー、音量、自動再生、キュー保持
- [開発・運用手順](docs/development.md)：起動・停止・再起動、検証タスク、yt-dlp更新
- [現行アーキテクチャ](docs/architecture.md)：音声パイプラインと依存関係
- [文書一覧](docs/index.md)：詳細仕様、検証・移行の記録
- [AGENTS.md](AGENTS.md)：開発エージェントの操作規則

## 開発環境を用意する

Windows x64とLinux x64（glibc）を対象にしています。標準ビルドは`GOAMD64=v3`を使うため、実行には対応CPUが必要です。開発依存とタスクはmiseで管理し、`mise.lock`を有効にしています。Go依存は`go.mod` / `go.sum`で固定します。

```text
mise trust
mise install --locked
mise run doctor
mise run check
```

mise 2026.10.3で動作確認しています。タスク本体はGoで実装し、シェル・PowerShellスクリプトは使用しません。

| 依存 | 用途 |
| --- | --- |
| Go | Bot・開発タスクのビルド |
| yt-dlp、Deno、FFmpeg | サイト対応、音源取得、入力音声のデコード |
| Zig、公式libdave | Zigはrace検証、公式libdaveは相互運用・比較検証用。標準Botのビルドには不要 |
| Python、Bazel | ViSQOL検証用。標準Botの実行には不要 |

上の`mise install --locked`は比較検証用も含むプロジェクトの開発依存を導入します。ViSQOL本体のビルドなど、追加の準備は[ViSQOL検証記録](docs/visqol-comparison.md)を参照してください。

## 接続を設定する

Botをサーバーへインストールし、`asterinmusic.example.yaml`をGit管理外の`asterinmusic.yaml`へコピーして編集します。TokenはDiscord開発者ポータルのBot画面、Application IDはGeneral Information、サーバーIDはDiscordの開発者モードから取得します。[設定ファイルの仕様](docs/configuration.md)を参照してください。

```toml
[env]
ASTERINMUSIC_BOT_MAIN_TOKEN = ""
```

上記は開発用`mise.local.toml`の例です。配布時はプロセス環境変数、または明示指定したdotenvからトークンを供給できます。YAMLに直接記述することも可能です。設定した1サーバーでコマンドを受け付けます。インストール時のスコープと権限は[リスト追加の詳細](docs/playlist-button.md#discord権限)に記載しています。

```text
mise run config-check
mise run discord-check
mise run register
mise run bot-start
mise run bot-status
```

`register`は対象サーバーにコマンドを登録します。`bot-start`はビルド後にバックグラウンドで起動し、Discord接続完了を待ちます。起動済みなら追加起動しません。`serve`は`bot-start`の別名です。

## 再生する

自分の次の番に聴きたい曲は`/playnext url:URL`で追加できます。自分の待機曲の先頭に入り、他の追加者との交代順と再生中の曲は維持します。

音声チャンネルに参加し、`/play url:URL`で曲を追加します。初期音量は30%（−28dB）です。`/queue show`で待機順、`/nowplaying`で再生中の曲を確認できます。変更操作は原則としてBotと同じ音声チャンネルから行います。

`/stop`は再生中の曲を終了して切断し、待機キューを保持します。URLなしの`/play`でキューを再開できます。全コマンドと動作の詳細は[現行仕様](docs/specification.md)を参照してください。

## 開発中に更新する

```text
mise run check
mise run bot-restart
mise run bot-status
mise run bot-logs
```

`bot-restart`は新版のビルドが成功してから旧版の状態を保存し、新版を起動します。キュー、音量、自動再生設定を復元し、再生中だった場合は再接続して曲の先頭から再開します。停止中は停止を維持します。旧版からの初回移行や接続失敗時は、URLなしの`/play`で再開してください。

停止は`mise run bot-stop`、再起動は`mise run bot-restart`です。プロセス調査の失敗や管理外Botを検出した場合は、追加起動や強制終了で回避せず、[開発・運用手順](docs/development.md)に従って確認してください。
