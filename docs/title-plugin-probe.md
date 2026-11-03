# YouTubeタイトル補正プラグインの検証

2026-11-03時点の隔離実験。Botの実行経路には組み込んでいない。

## 結果

Windowsのmise固定版yt-dlp 2026.08.19（公式exe）で、約30行のextractorプラグインを読み込めた。別途Pythonのインストールやyt-dlp本体の変更は不要だった。検証タスクはGoで実装している。

`mise run title-plugin-probe`で再実行できる。公開動画のメタデータのみ取得し、Discord接続や音源ダウンロードは行わない。既定のプラグイン探索先を無効にしてから実験用ディレクトリを明示する。各ケースの上限は60秒、Mixは先頭3項目。標準出力はUTF-8のJSON Lines。日本語補正ケースは「青春病」が得られなければ失敗する。

| ケース | 結果 |
| --- | --- |
| QPLviaYkuSs、プラグインなし、ja | `Fujii Kaze - "Seishun Sick" (live at Nissan Stadium) [Day2]` |
| 同動画、プラグインあり、ja、YouTube Music URL | `藤井 風 - "青春病" (Live at Nissan Stadium) [Day2]` |
| 同動画、プラグインあり、en | 英語タイトル |
| 同動画、プラグインあり、言語指定なし | 従来と同じ英語タイトル |
| PE_iyDsY5xM、プラグインあり、ja | `初星学園 「一体いつから」Official Music Video (HATSUBOSHI GAKUEN - Ittai itsukara)` |
| RDQPLviaYkuSs、flat取得、ja | プラグイン有無とも起点曲が日本語。候補はリクエストごとに変動 |

## 仕組みと保守範囲

公式の`plugin_name`によるYoutubeIE置換機構を使う。`_download_initial_data`が既に取得したデータから、画面表示用の`videoPrimaryInfoRenderer.title`を動画IDとともに保持する。`_real_extract`の結果が同じ動画IDの単曲で、かつ言語が指定されている場合だけ`title`を置換する。タイトルが見つからなければ元の結果を維持する。抽出終了時に保持データを消去する。

プラグインに通信処理はなく、取得済みデータを再利用する。形式・音声URL・再生時間は変更しない。今回の実行はskip-downloadのため、音声取得そのものの回帰検証ではない。

プラグインの読み込みは公式機能だが、上書きする2メソッドと`_preferred_lang`、`_get_text`は内部APIであり互換性保証はない。YouTubeのJSON構造にも依存する。構造からタイトルが消える場合は元のタイトルへ戻るが、内部API変更時は起動・抽出エラーになり得る。yt-dlpの更新候補に対してこの検証を通してから切り替える運用が必要。

flatプレイリスト・MixはYoutubeTab系の別経路であり、このプラグインは補正しない。今回のMixでは元から日本語が返った。通常プレイリスト全般、他言語全般、Linux実行、最新yt-dlpへの互換性、欠落データの自動回帰テストは今回の確認範囲外。

結論は「小さなプラグインとして維持する見込みはあるが、更新時検証を条件とする」。本採用時はメタデータ取得だけに明示的に読み込み、音声取得経路には渡さず、プラグインを無効化できるようにする。既存キューのタイトルを自動更新する機能は別途必要。

## 内部APIのGit履歴調査

2026-11-03に公式リポジトリを取得。確認したHEADは`b77c9304e1c4220cca450c583491904daf8455af`（2026-10-29）。直近1年は2025-11-03以降を対象に、`git log -G`で定義行、`git log -L`で対象メソッドの範囲を確認した。ファイル変更数は`git rev-list --count`による。変更数は破壊的変更数ではない。

| 対象 | 直近1年の定義行変更 | 同期間のメソッド本体変更 |
| --- | --- | --- |
| `_download_initial_data` | 0 | 1 |
| `_real_extract`（YoutubeIE） | 0 | 11 |
| `_preferred_lang` | 0 | 0 |
| `_get_text` | 0 | 0 |

同期間のファイル全体の変更は`_video.py`が47コミット、`_base.py`が22コミット（両方に触れたコミットは重複する）。抽出器全体の更新頻度を、そのままプラグインの修正頻度とは見なせない。

- [2025-07-11: 初期データ取得をメソッド化](https://github.com/yt-dlp/yt-dlp/commit/5b57b72c1a7c6bd249ffcebdf5630761ec664c10)。`_download_initial_data(self, video_id, webpage, webpage_client, webpage_ytcfg)`が導入された。導入から確認HEADまで引数変更は見つからない。導入前のバージョンでは今回のフックは呼ばれない。
- [2026-03-03: ページ応答の利用条件を変更](https://github.com/yt-dlp/yt-dlp/commit/2ecc4c3bc)。同メソッド内のスキップ判定を`_configuration_arg('webpage_skip')`から`_skipped_webpage_data`へ変更。引数・戻り値を維持し、今回の上書き方法に直接の修正は不要な差分。
- [2026-10-21: 初期データ抽出を修正](https://github.com/yt-dlp/yt-dlp/commit/12ac9aca79671420724227aca37bc888c604a6f8)。YouTubeの別形式のscript要素にも対応。プラグインは解析済みデータを受け取るため、この種のHTML抽出修正は上流に任せられる。ただし内部JSONの表示タイトル構造は別の依存点として残る。
- [2025-03-13: YouTube抽出器をパッケージ化](https://github.com/yt-dlp/yt-dlp/commit/4432a9390)。大きな再構成の実例。ただし試作で使う`from yt_dlp.extractor.youtube import YoutubeIE`は分割後も使用できる。

判断は「今回のフックの引数は頻繁に変更されていない」。一方、内部APIの将来の互換性は保証されず、取得経路とYouTube応答も変化する。本採用ではバージョン固定、更新前の実動画検証、補正不能時の元タイトルへのフォールバックを継続する。抽出本体をコピーする方式より、上流を呼び出して結果だけ補正する試作方式のほうが追従箇所を限定できる。

## 検索による追加候補

yt-dlp の `ytsearch` とプレイリストフィルター付き検索URLで候補を取得した。言語は `youtube:lang=ja`。検索結果のタイトルは個別取得の期待値を保証しない。

| 動画ID | 標準の個別取得 | プラグインの個別取得 | 用途 |
| --- | --- | --- | --- |
| `SfPkl7lol7g` | Fujii Kaze - Hana (Official Video) | 藤井 風 - 花 (Official Video) | 補正成功の追加例。music.youtube.comで確認 |
| `TcLLpZBWsck` | Fujii Kaze - Kirari (Official Video) | Fujii Kaze - Kirari (Official Video) | 検索では日本語だが、今回の補正では日本語にならない例 |
| `ZRtdQ81jPUQ` | YOASOBI「アイドル」 Official Music Video | 同左 | 既存の日本語タイトルを維持する例 |

- 公開プレイリスト: https://www.youtube.com/playlist?list=PLy7-V7qub3dv9TGthpOoVKqNND1kxOkFA 。検索で発見し、標準のflat取得で先頭5件を確認。対象動画の収録は未確認。タイトル一覧が取得できることの確認に使う。
- Mix: https://www.youtube.com/watch?v=TcLLpZBWsck&list=RDTcLLpZBWsck 。検索で見つけた動画IDから構成し、標準のflat取得で先頭5件を確認。先頭の「きらり」は英語、後続の「まつり」などは日本語だった。
- 英語タイトルの比較候補: https://www.youtube.com/watch?v=dQw4w9WgXcQ 。検索で取得できたが、今回の個別比較は未実施。

リストとMixの全件比較、言語別比較、ライブ取得を固定期待値にしないオフライン回帰テストは別途必要。

## 「きらり」の追加調査

`TcLLpZBWsck` は `youtube:lang=ja` でも `videoPrimaryInfoRenderer.title` が英語だった。`webpage_skip=initial_data` でページ内データを使わずnext APIから取得しても同じだった。このため、既存のページ内タイトル補正だけでは日本語化できない。

一方、`ytsearch3:TcLLpZBWsck` は、ja指定で同じ動画IDの日本語タイトル、en指定で英語タイトルを返した。実験プラグインに `youtube:lang=ja;asterin_title_search=true` で明示的に有効化する検索補正を追加した。`YoutubeSearchIE.extract` を呼び出し、最大3件の結果から動画IDが完全一致したものだけ採用する。検索失敗・一致なしでは補正前のタイトルを維持する。言語未指定では検索しない。検索結果を個別抽出へ再投入しないため再帰呼び出しは起こさない。

ライブ確認では「きらり」「花」「青春病」がjaで日本語、enで英語になった。Rick Astleyの英語タイトルはja/enとも維持された。`title-plugin-probe` に「きらり」のja/enと英語曲のjaを追加した。

この検索補正は本番未適用。言語指定時に検索を追加するため遅延・リクエスト数が増える。検索インデックスの鮮度や検索不能な限定公開動画にも制約がある。今回の実装では日本語が既に得られていても明示的に有効なら検索する。文字種だけで翻訳の要否を判定することはしない。flat取得のプレイリスト・Mixにはこの動画抽出フックが適用されず、全曲の補正には別設計が必要になる。検索失敗・ID不一致のオフラインテスト、キャッシュやタイムアウト予算は本番導入前の課題とする。

## YouTube Data APIによる比較

`mise run youtube-data-probe` はGit管理外の `.env.youtube` の `YOUTUBE_DATA_API_KEY` を読み、公式 `videos.list` に5動画IDをまとめて指定する。キーはヘッダーで渡し、ログには出さない。ja・en・言語指定なしの3リクエストで全動画を取得できた。初回はfieldsにエラー項目も指定してHTTP 400となったため、itemsだけに修正して再実行した。

| 動画 | jaのlocalized.title | en / 言語未指定 |
| --- | --- | --- |
| きらり | 藤井 風 - "きらり" Official Video | Fujii Kaze - Kirari (Official Video) |
| 花 | 藤井 風 - 花 (Official Video) | Fujii Kaze - Hana (Official Video) |
| 青春病 | 藤井 風 - "青春病" (Live at Nissan Stadium) [Day2] | Fujii Kaze - "Seishun Sick" (live at Nissan Stadium) [Day2] |
| Rick Astley | 英語タイトルを維持 | 同左 |
| YOASOBI「アイドル」 | 日本語タイトルを維持 | 同左 |

`snippet.title` はja指定でも最初の3曲が英語であり、採用するのは `snippet.localized.title` とする必要がある。「きらり」のdefaultLanguageはjaだったが、通常titleは英語であり、defaultLanguageのみから文字列の言語は判断しない。

本番への組み込み、キャッシュ、障害時フォールバック、リスト・Mixからの一括補完は未実装。稼働中Botの変更・再起動は行っていない。

## 参照リンク

- [yt-dlp公式プラグイン仕様](https://github.com/yt-dlp/yt-dlp#developing-plugins)
- [関連Issue #13363](https://github.com/yt-dlp/yt-dlp/issues/13363)
- [関連PR #13374（未マージ）](https://github.com/yt-dlp/yt-dlp/pull/13374)
