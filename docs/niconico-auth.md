# ニコニコ動画の認証

ニコニコ動画のログイン必須の通常動画を取得するため、管理者が1インスタンスにつき1アカウントを設定できる。利用者ごとのログイン、有料・会員限定動画、ニコニコ生放送は対象外。YouTubeへの取得処理には認証情報を渡さない。

## Cookieファイル方式

既存のYAMLへ以下を追加する。`sources`があれば、その中へ統合する。

```yaml
sources:
  niconico:
    auth:
      method: cookies
      cookies_file: ./.dev/secrets/niconico.cookies.txt
```

相対パスの基準は設定ファイルのディレクトリ。配布環境では任意の保護されたパスを指定でき、`${NICONICO_COOKIES_FILE}`などの変数参照も使える。`username`と`password`は記述しない。

CookieファイルはNetscape形式、最大4MiBとする。起動時は読み取りと形式を検証するだけで、オンラインログインは行わない。取得時に有効期限内またはセッションCookieの`user_session`を確認する。期限内でもサーバー側で失効している場合があるため、有効期間は保証しない。

ファイルは各取得時に読み直す。元ファイルは書き換えず、ニコニコのドメインに限定した一時コピーをyt-dlpへ渡し、処理終了・失敗・キャンセル時に削除する。同じパスへの差し替えは次の取得から反映される。ファイルを編集中に取得されないよう、新しいファイルを用意して置換する。

## Chromeからの保存

1. Chromeでニコニコ動画へログインし、対象動画がブラウザーで再生できることを確認する。ブラウザー上では必要に応じて2段階認証を完了する。
2. [yt-dlp公式FAQ](https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp)から案内されている[Get cookies.txt LOCALLY](https://chromewebstore.google.com/detail/get-cookiestxt-locally/cclelndahbckbenkjhflpdbgdldlbecc)をChromeへ追加する。
3. `www.nicovideo.jp`の動画タブで拡張機能を開き、現在のサイトのCookieをNetscape形式でエクスポートする。全サイトのエクスポートは選ばない。HttpOnly Cookieも含める。
4. 開発環境ではプロジェクト内に`.dev/secrets`フォルダーを作り、`niconico.cookies.txt`として保存する。この場所はGit管理外。ファイルの内容は共有・コミットしない。
5. 上記のYAMLを設定し、次の診断と再起動を行う。

```text
mise run config-check
mise run source-check -- https://www.nicovideo.jp/watch/sm46647529
mise run bot-restart
mise run bot-status
```

`source-check`はDiscordに接続せず、設定した認証方式でメタデータと音声全体を取得・破棄する。上限は256MiB・5分。既存の`inspect`や`capture`は設定を読まない独立診断のため、認証の確認には使わない。

Cookieはログイン状態を利用できる秘密情報として扱い、Botの実行ユーザーだけが読み取れる場所に保存する。失効した場合はChromeでログイン状態を確認して再度エクスポートする。AsterinMusicはChromeのプロファイルを直接読み取らない。

## ID・パスワード方式

```yaml
sources:
  niconico:
    auth:
      method: password
      username: "${NICONICO_USERNAME}"
      password: "${NICONICO_PASSWORD}"
```

変数はプロセス環境変数、開発時の`mise.local.toml`、明示指定したdotenvから供給できる。`cookies_file`は記述しない。2段階認証・CAPTCHA・メール確認などの追加認証には対応しない。追加認証が必要ならブラウザーでログインを完了し、Cookie方式を使用する。

初回取得時にyt-dlpのニコニコログイン処理を呼ぶ。ID・パスワードは専用の一時netrcファイルへ書き、コマンドライン引数には含めない。標準入力は接続せず、2段階認証コードを空値で指定して対話入力を待たせない。ログイン処理には45秒の上限がある。

取得したセッションはメモリ内だけに保存し、メタデータ・音声取得で共有する。同時ログインは直列化するが、認証後の音声取得は直列化しない。再起動するとセッションは破棄される。セッション失効を識別できた場合は1回だけ再ログイン・再取得する。ただし音声の一部を出力済みなら、重複再生を防ぐため同じ出力先へ再試行しない。

認証失敗・追加認証・再ログイン失敗では、そのプロセスでの自動ログインを停止する。設定を修正してBotを再起動すると解除される。通信障害や動画削除を認証失敗とは判定しない。認証方式の自動切り替えは行わない。

## エラーと秘密情報

認証が必要、Cookieの更新が必要、ログイン設定の見直しが必要という案内をDiscordへ返す。認証付き取得のyt-dlpエラー原文は、Cookieやアカウント情報が含まれる可能性があるためログ・Discordへ出さない。認証情報はキューや再生状態ファイルへ保存しない。

一時認証ディレクトリはLinuxでは0700、ファイルは0600。Windowsでは継承を無効化し、実行ユーザーとSYSTEMだけにアクセスを許可するACLを設定してから秘密情報を書き込む。正常終了・失敗・キャンセルでは削除するが、OS停止や強制終了時には残る可能性がある。

自動テストは認証の分離、差し替え、セッション再利用、失効時の再試行制限、並行処理、キャンセル、エラー秘匿を確認する。実アカウントでのログイン・対象動画の取得は、利用者が認証情報を設定したうえで別途確認する。

実装時の確認では`mise run check`（テスト・vet・Windows/Linuxビルド）とLinuxでの`mise run test -- -race`が通過した。Windowsの`race-test`はThreadSanitizerのメモリ確保エラーで起動できなかった。Pythonのnetrcパーサーで引用符・バックスラッシュを含むテスト用パスワードの復元も確認した。`sm46647529`への認証なしの`source-check`ではログインを促すエラーを確認した。その後、利用者がChromeから保存したCookieで同じ動画のメタデータ・音声全体の取得に成功し、管理再起動後のBotが`ready=true`となることを確認した。ID・パスワード方式での実アカウントのログイン成功と、当該動画のDiscord上での試聴は未確認。
