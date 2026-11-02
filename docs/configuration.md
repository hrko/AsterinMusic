# 設定ファイル

RainMusicはkoanf v2でYAMLを読み込み、文字列値を展開してから型付き設定へ変換・検証する。初期版は1Bot・1サーバーのみ実行可能。複数指定は未対応エラーとなり、一部だけを使用することはない。

`rainmusic.example.yaml`を`rainmusic.yaml`へコピーして編集する。指定がなければカレントディレクトリの`rainmusic.yaml`を読む。別の場所は`--config PATH`で指定する。欠落・構文不正はエラーで、自動探索や旧環境変数へのフォールバックは行わない。

## 構造

| 項目 | 必須・既定値 |
| --- | --- |
| `version` | 必須、`1` |
| `runtime.state_dir` | `./.rainmusic/state` |
| `tools.yt_dlp.path` | `yt-dlp` |
| `tools.ffmpeg.path` | `ffmpeg` |
| `playback.default_volume` | `30`、0〜100 |
| `playback.queue_limit` | `500`、正の整数 |
| `bots.<name>.application_id` | 任意。認証結果と照合し、省略時は認証したIDを使用 |
| `bots.<name>.token` | 必須。直接記述または変数参照 |
| `bots.<name>.activity.enabled` | `true`。共有・設定外参加・参加状況未確定では表示不可 |
| `servers.<name>.id` | 必須。引用符付きDiscord ID |
| `servers.<name>.bots` | 必須。使用可能なBot名のリスト |
| `servers.<name>.command_bot` | 必須。上記リスト内のBot名 |

配置関係は`servers.*.bots`だけに記述する。Bot名・サーバー名は英小文字・数字・`_`・`-`を使用する。未知のキー、重複キー、不正な型、null、YAMLのエイリアス・マージキー、複数ドキュメントを拒否する。リストは全体置換であり、連結しない。ファイルは1つだけ読み、includeは提供しない。

## 変数と認証情報

`token: "${RAINMUSIC_BOT_MAIN_TOKEN}"`のように参照する。`--env-file PATH`を指定した場合だけdotenvを追加で読む。優先順位はプロセス環境変数、dotenvの順。環境変数が明示的に空の場合も優先し、必須トークンならエラーにする。dotenvは内部マップへ読み、`os.Setenv`は呼ばない。

YAML解析後、文字列値（配列内を含む）で`${NAME}`を1回だけ展開する。キーは展開せず、展開結果を再解析・再展開しない。`$$`はリテラルの`$`。変数名は`[A-Za-z_][A-Za-z0-9_]*`。未定義、不正な構文、`$VAR`、既定値構文、コマンド置換はエラー。数値や真偽値への暗黙変換は行わない。

dotenv内部の引用符・展開はkoanfのdotenvパーサー（godotenv）の規則に従う。YAML側の展開とは別段階であり、dotenvで得た値を挿入後に再展開しない。値をそのまま保持したい場合はdotenv内でシングルクォートを使う。

直接トークンを書いてもよいが、設定ファイルも秘密情報として扱う。設定全体・解析エラー原文・値はログに出さない。配布例にはトークンを含めない。`rainmusic.yaml`と`.env`はGit管理外。別名ファイルを使う場合も利用者が管理対象外とアクセス権を設定する。

## パスと実行状態

CLIのファイル指定は起動時のカレントディレクトリ、YAML内の相対パスは設定ファイルのディレクトリを基準にする。変数展開後に解決する。実行ファイル名だけならPATH検索、パスを含むならファイルとして解決する。`~`は展開しない。

yt-dlpが既定の`yt-dlp`の場合、従来の更新管理で選択済みのバージョンがあればそれを使用する。明示的な実行ファイルパスは更新管理の選択より優先する。FFmpeg・yt-dlpの実行ファイルの存在は起動前に確認する。

設定は起動時のみ読み込む。再生中の音量・キュー・自動再生は別途永続化し、設定には書き戻さない。保存済み音量は初期音量より優先。キュー上限を減らしても既存曲を削除せず、上限未満になるまで追加を拒否する。通常のVC・通知先はDiscord操作から決定する。

## 開発環境の移行

ローカルの`rainmusic.yaml`は`${DISCORD_BOT_TOKEN}`など既存のmise環境変数を明示参照し、`runtime.state_dir: ./.dev/state`として既存キューを維持する。環境変数名からの自動設定は廃止。miseは開発用の環境変数供給・タスクランナーであり、配布バイナリの必須依存ではない。

```text
mise run config-check -- --config rainmusic.yaml
mise run discord-check -- --config rainmusic.yaml
mise run bot-restart -- --config rainmusic.yaml --env-file .env
```

`config-check`はオフラインの解析・検証、`discord-check`は認証・サーバーアクセス・ツール存在・保存済み状態の読み込み検証を行う。認証中はGatewayへ接続しない。

管理再起動は新版をビルドして事前検証し、成功してから旧Botへ停止を依頼する。設定指定を省略した再起動は稼働中Botのファイル指定を引き継ぐ。明示指定時は新しい指定一式を使う（dotenv省略はdotenvなし）。停止後の新規起動は再度指定するか既定ファイルを使う。

診断用チャンネルは`mise run voice-check -- --voice-channel ID`、または`mise run discord-check -- --voice-channel ID --text-channel ID`で指定する。旧`DISCORD_TEXT_CHANNEL_ID`・`DISCORD_VOICE_CHANNEL_ID`は自動参照しない。音源単独の`inspect`・`render`等は従来どおりPATH上のツールを使う独立診断コマンド。
