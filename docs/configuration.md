# 設定ファイル

AsterinMusicはkoanf v2でYAMLを読み込み、文字列値を展開してから型付き設定へ変換・検証する。初期版は1Bot・1サーバーのみ実行可能。複数指定は未対応エラーとなり、一部だけを使用することはない。

`asterinmusic.example.yaml`を`asterinmusic.yaml`へコピーして編集する。指定がなければカレントディレクトリの`asterinmusic.yaml`を読む。別の場所は`--config PATH`で指定する。欠落・構文不正はエラーで、自動探索や旧環境変数へのフォールバックは行わない。

## 構造

名称変更前の`rainmusic.yaml`を使っている場合は、`asterinmusic.yaml`へ改名するか、`--config`で明示する。既定の状態保存先も`.rainmusic/state`から`.asterinmusic/state`へ変更したため、既存のキューを引き継ぐ場合は`runtime.state_dir`に従来の保存先を指定する。開発環境の`.dev/state`はそのまま使用できる。開発用の`RAINMUSIC_*`環境変数は`ASTERINMUSIC_*`へ改名する。YAML内の`${DISCORD_TOKEN}`など、利用者が指定する認証情報の変数名は変更不要。

| 項目 | 必須・既定値 |
| --- | --- |
| `version` | 必須、`1` |
| `logging.level` | `info`。`debug` / `info` / `warn` / `error`（大文字も可） |
| `logging.format` | `text`。`text` / `json` |
| `logging.components` | 空。コンポーネント名をキー、上書きレベルを値とするマップ |
| `runtime.state_dir` | `./.asterinmusic/state` |
| `tools.yt_dlp.path` | `yt-dlp` |
| `tools.ffmpeg.path` | `ffmpeg` |
| `playback.default_volume` | `30`、0〜100 |
| `playback.queue_limit` | `500`、正の整数 |
| `history.limit_per_voice` | `10000`、1〜1,000,000。VCごとの再生履歴保持件数。次回の履歴追加時に古い分を整理 |
| `playback.disconnect_when_idle` | `false`。`true`の場合のみ再生待ち60秒で退出。無人60秒での退出は設定に関係なく有効 |
| `sources.youtube.metadata_language` | `ja`。YouTube / YouTube Musicのメタデータ優先言語。`provider_default`または`auto`でyt-dlpの既定動作 |
| `sources.niconico.auth.method` | `none`（省略時）、`cookies`、`password` |
| `sources.niconico.auth.cookies_file` | `cookies`時のみ必須。設定ファイル基準のパス |
| `sources.niconico.auth.username` / `password` | `password`時のみ両方必須。文字列・変数参照 |
| `bots.<name>.application_id` | 任意。認証結果と照合し、省略時は認証したIDを使用 |
| `bots.<name>.token` | 必須。直接記述または変数参照 |
| `bots.<name>.activity.enabled` | `true`。共有・設定外参加・参加状況未確定では表示不可 |
| `servers.<name>.id` | 必須。引用符付きDiscord ID |
| `servers.<name>.bots` | 必須。使用可能なBot名のリスト |
| `servers.<name>.command_bot` | 必須。上記リスト内のBot名 |

配置関係は`servers.*.bots`だけに記述する。Bot名・サーバー名は英小文字・数字・`_`・`-`を使用する。未知のキー、重複キー、不正な型、null、YAMLのエイリアス・マージキー、複数ドキュメントを拒否する。リストは全体置換であり、連結しない。ファイルは1つだけ読み、includeは提供しない。

## ログ設定

`logging`を省略した既存設定はINFO・textで動作する。指定できるコンポーネントは`app`、`voice`、`playback`、`source`、`history`、`state`、`interaction`、`updater`、`discord`、`dave`。例えば`logging.components.voice: debug`を指定すると、他のコンポーネントをINFOに保ったまま音声接続の診断を有効にできる。未知の名前・不正なレベル・形式は起動時に拒否する。変更には再起動が必要である。

ログは標準エラーへ出力する。JSONは一行一イベントで、診断コマンドが標準出力へ返す結果とは分離する。秘密情報はDEBUGでも出力対象にしない。依存ライブラリの原文・任意属性は除外し、コンポーネントとレベル、安全な数値属性だけを残すため、DEBUGにしてもライブラリの通信内容全体は表示されない。

移行範囲と検証状況は[ログ改善の要件](logging-plan.md)を参照する。

## 変数と認証情報

`token: "${ASTERINMUSIC_BOT_MAIN_TOKEN}"`のように参照する。`--env-file PATH`を指定した場合だけdotenvを追加で読む。優先順位はプロセス環境変数、dotenvの順。環境変数が明示的に空の場合も優先し、必須トークンならエラーにする。dotenvは内部マップへ読み、`os.Setenv`は呼ばない。

YAML解析後、文字列値（配列内を含む）で`${NAME}`を1回だけ展開する。キーは展開せず、展開結果を再解析・再展開しない。`$$`はリテラルの`$`。変数名は`[A-Za-z_][A-Za-z0-9_]*`。未定義、不正な構文、`$VAR`、既定値構文、コマンド置換はエラー。数値や真偽値への暗黙変換は行わない。

dotenv内部の引用符・展開はkoanfのdotenvパーサー（godotenv）の規則に従う。YAML側の展開とは別段階であり、dotenvで得た値を挿入後に再展開しない。値をそのまま保持したい場合はdotenv内でシングルクォートを使う。

直接トークンを書いてもよいが、設定ファイルも秘密情報として扱う。設定全体・解析エラー原文・値はログに出さない。配布例にはトークンを含めない。`asterinmusic.yaml`と`.env`はGit管理外。別名ファイルを使う場合も利用者が管理対象外とアクセス権を設定する。

## パスと実行状態

### YouTube Data APIによるタイトル補完

同じリクエストで `contentDetails.duration`、チャンネル名・ID、サムネイルも取得する。時間はyt-dlpの有効値を優先し、APIの正の有限値で欠落を補完する。サムネイルはmedium、default、high、standard、maxresの順で取得済みHTTPS URLを選び、Botではダウンロードしない。各項目は独立して補完する。キャッシュとキュー保存にも追加項目を含め、表示時のAPI呼び出しは行わない。旧キューは欠落項目を省略表示し、自動一括更新しない。

`sources.youtube.data_api.enabled` は既定で `false`。有効時は空でない `sources.youtube.data_api.api_key` が必須で、`${YOUTUBE_DATA_API_KEY}` など既存の変数展開を利用できる。dotenvは `--env-file` で明示する。APIキーを含む設定やHTTPエラー原文はログへ出力しない。

言語は `metadata_language` と共通。`auto` / `provider_default` ではAPI補完を行わない。指定言語がある場合は `videos.list` の `snippet.localized.title` で表示タイトルを補完する。独自翻訳は行わず、動画が返らない・空タイトル・API障害の場合はyt-dlpのタイトルを維持する。認証やクォータ等のHTTP 400/401/403/429では15分、その他の障害では30秒問い合わせを抑止する。キャッシュ済みのタイトルは抑止中も利用できる。補完失敗の曲別通知は出さず、安全なステータスのみログへ記録する。

単曲・プレイリスト・Mix・自動再生候補の新規取得に適用する。音声取得や候補選択は変更しない。1操作の補完待ちは最大3秒（同時処理待ちを含む）、50件単位で一括取得し、即時リトライしない。動画IDと言語で24時間・最大10,000件のメモリキャッシュを持つ。APIから動画が返らなかった場合は5分だけ保持する。タイトルが空でも他の取得済み項目は独立して利用する。再起動でキャッシュは失われるが、キューの表示タイトル・取得元・言語は保存される。既存キューは一括更新しない。設定変更は再起動後に適用する。

APIキーは利用者自身のGoogle CloudプロジェクトでYouTube Data API v3を有効にして取得する。API制限にはYouTube Data API v3を指定する。既定クォータはプロジェクトで共有される。実験用の `.env.youtube` は自動ロードされない。本番では指定するdotenvまたはプロセス環境変数にキーを設定する。

`sources.youtube.metadata_language`は単曲・プレイリスト・Mix・自動再生候補の新しいメタデータ取得に適用する。`ja`、`en`、`zh-TW`など、yt-dlpが対応するコードを指定する。設定時にコードの構文を検証し、対応していないコードは取得時にyt-dlpがエラーにする。`provider_default`とその別名`auto`は言語オプションを省略する（動画の原語やDiscord利用者の言語を自動選択する意味ではない）。日本語が提供されない場合も翻訳は行わず、取得できたタイトルを使う。Botの応答・音声・字幕の言語は変更しない。変更にはBot再起動が必要で、保存済みキューの曲名は自動更新しない。

ニコニコ動画の認証方式は排他的に指定する。他方式の項目は空文字でも拒否する。Cookieファイルの読み取り・Netscape形式は設定検証時に確認し、オンライン認証は取得時に行う。ID・パスワードは空値と改行・NULを拒否する。Cookieの用意と設定例は[ニコニコ動画の認証](niconico-auth.md)を参照。

Cookieファイルの内容は各取得時に読み直すため、同じパスへの差し替えは再起動不要。ファイルパスや認証方式の変更には再起動が必要。

CLIのファイル指定は起動時のカレントディレクトリ、YAML内の相対パスは設定ファイルのディレクトリを基準にする。変数展開後に解決する。実行ファイル名だけならPATH検索、パスを含むならファイルとして解決する。`~`は展開しない。

yt-dlpが既定の`yt-dlp`の場合、従来の更新管理で選択済みのバージョンがあればそれを使用する。明示的な実行ファイルパスは更新管理の選択より優先する。FFmpeg・yt-dlpの実行ファイルの存在は起動前に確認する。

設定は起動時のみ読み込む。再生中の音量・キュー・自動再生は別途永続化し、設定には書き戻さない。保存済み音量は初期音量より優先。キュー上限を減らしても既存曲を削除せず、上限未満になるまで追加を拒否する。通常のVC・通知先はDiscord操作から決定する。

## 開発環境の移行

認可設定は`servers.<名前>.authorization`に指定する。`preset`は`collaborative`（既定）または`dj`。`dj_role_ids`は引用符付きDiscordロールIDの配列で、DJ方式では1件以上必要。`policies`は`id`と`policy`を持つ配列で、`policy`にCedarのpermitまたはforbidを1つ記述する。[設定例・許可表・上限](authorization-model.md)を参照。

ルールの不正・未知のキー・型違いは`config-check`と管理再起動の事前検証で拒否する。ルールの部分適用や許可の広い既定値への代替は行わない。変更には再起動が必要。認可エンジン、外部ルールファイル、実行中の編集UIは初期版では選択しない。

ローカルの`asterinmusic.yaml`は`${DISCORD_BOT_TOKEN}`など既存のmise環境変数を明示参照し、`runtime.state_dir: ./.dev/state`として既存キューを維持する。環境変数名からの自動設定は廃止。miseは開発用の環境変数供給・タスクランナーであり、配布バイナリの必須依存ではない。

```text
mise run config-check -- --config asterinmusic.yaml
mise run discord-check -- --config asterinmusic.yaml
mise run bot-restart -- --config asterinmusic.yaml --env-file .env
```

`config-check`はオフラインの解析・検証、`discord-check`は認証・サーバーアクセス・ツール存在・保存済み状態の読み込み検証を行う。認証中はGatewayへ接続しない。

管理再起動は新版をビルドして事前検証し、成功してから旧Botへ停止を依頼する。設定指定を省略した再起動は稼働中Botのファイル指定を引き継ぐ。明示指定時は新しい指定一式を使う（dotenv省略はdotenvなし）。停止後の新規起動は再度指定するか既定ファイルを使う。

診断用チャンネルは`mise run voice-check -- --voice-channel ID`、または`mise run discord-check -- --voice-channel ID --text-channel ID`で指定する。旧`DISCORD_TEXT_CHANNEL_ID`・`DISCORD_VOICE_CHANNEL_ID`は自動参照しない。音源単独の`inspect`・`render`等は従来どおりPATH上のツールを使う独立診断コマンド。
