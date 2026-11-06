# ソース側ラウドネス情報の検証

2026-11-07に実施。目的は、YouTube／YouTube Musicとニコニコ動画の間でも聴感上の音量を近づけるために、ソース側の測定情報を使えるか確認すること。Botの再生処理は変更していない。

## 方法

隔離したyt-dlpプラグインで内部応答のラウドネス項目を抽出し、実際に選択された音声を全曲ダウンロードしてFFmpegで測定した。yt-dlpは2026.08.19、FFmpegは8.1.2。YouTubeはitag 251、ニコニコは下表のAAC形式を取得した。

測定前に48 kHz・ステレオへ変換し、`loudnorm`の`input_i`（統合ラウドネス）と`input_tp`（トゥルーピーク）を採用した。出力JSONの`output_*`はフィルター自身の動的補正結果であり、ソース情報に基づく固定ゲインの検証結果ではない。今回は固定ゲイン適用後の再測定・Discordでの試聴は実施していない。

## 結果

ソース値と全曲実測の差は6曲とも0.10 LU以内だった。これはこの音源・形式での結果であり、全動画での取得や精度を保証しない。

| サイト・動画 | ソースの統合ラウドネス | 実測 LUFS | 実測 dBTP | 形式 |
| --- | ---: | ---: | ---: | --- |
| [YouTube・きらり](https://www.youtube.com/watch?v=TcLLpZBWsck) | -8.60 | -8.63 | +1.58 | 251 |
| [YouTube Music・青春病 Live](https://music.youtube.com/watch?v=QPLviaYkuSs) | -12.49 | -12.53 | +0.54 | 251 |
| [YouTube・一体いつから](https://www.youtube.com/watch?v=PE_iyDsY5xM) | -8.61 | -8.65 | +1.00 | 251 |
| [ニコニコ・sm9](https://www.nicovideo.jp/watch/sm9) | -6.50 | -6.57 | +1.10 | AAC 128 kbps |
| [ニコニコ・sm2057168](https://www.nicovideo.jp/watch/sm2057168) | -9.90 | -9.98 | +1.04 | AAC 192 kbps |
| [ニコニコ・sm46647529](https://www.nicovideo.jp/watch/sm46647529) | -8.90011 | -9.00 | -1.09 | AAC 192 kbps |

YouTubeのソース値は`playerConfig.audioConfig.trackAbsoluteLoudnessLkfs`。同時に`perceptualLoudnessDb`も同じ値で取得できた。3曲とも`loudnessTargetLkfs=-14`だった。形式ごとの`loudnessDb`も取得でき、最後の曲では全体の5.39に対してitag 251は5.38だった。実装時は選択形式と対応させる必要がある。

ニコニコでは`media.domand.audios[]`の直下に`integratedLoudness`、`truePeak`、`loudnessCollection`があった。過去の応答例にあった`metadata`の下だけを探すと見つからない。`loudnessCollection`の`type=video`の係数は順に0.3758374、0.5559043、0.4954565で、`integratedLoudness + 20 log10(係数)`は3曲とも-15となった。今回の応答からはサイト側が-15を目標としていると推測できるが、公式の恒久的な仕様とは扱わない。

ニコニコの`truePeak`は順に+0.8、+0.9、-0.479089で、実測とは最大約0.61 dB異なった。ソース値だけで復号後のピークを保証できない。

## 設計への示唆

サイトの補正係数をそのまま流用すると、今回の応答ではYouTubeの-14とニコニコの-15という目標の違いが残る。Bot側に共通の目標値を持ち、原音の統合ラウドネスから`補正dB = 共通目標 − ソースの統合ラウドネス`を求める方式が有望。ユーザーの音量操作とは別の曲単位の補正として扱う。

例えば共通目標を仮に-18 LUFSとすると、上表の実測値から計算した補正後の推定値は-18.03〜-18.10 LUFSになる。これは計算上の予測であり、-18の採用決定や実音声での検証済みという意味ではない。サイト側補正が既に音声に適用されていないかも確認が必要だが、今回のダウンロード音声は補正前のソース値と一致した。

統合ラウドネスを合わせても、曲内の静かな区間・音色・ダイナミクスによる聴感差は残る。音量の完全一致は保証しない。次の検証では静かな曲・長い無音・トーク・ライブなどを追加し、固定ゲイン適用後の実測と試聴を行う。増幅時のピーク制限、値がない場合の実測フォールバック、取得失敗時の挙動は別途要件化する。

取得項目は内部APIに依存する。今回のプラグインは調査用であり、本番導入する場合は欠損・不正値・形式対応・API変更を扱う必要がある。

## 再実行

`mise run loudness-probe`で上記6曲、`mise run loudness-probe -- URL...`で指定音源を検証する。既存の`asterinmusic.yaml`と設定済みのニコニコCookieファイルを利用する。Cookieは一時コピーを使用し、元ファイルを変更しない。

音声とCookieコピーは終了時に削除し、抽出した項目・測定値のみ`.dev/loudness-probe/results.json`へ保存する。再実行で結果を上書きするため、比較時は先にコピーする。各サンプルの`error`も確認する。実験はDiscordに接続しない。取得は1曲5分・256 MiB、測定は1曲3分を上限とする。強制終了時は一時ファイルが残る可能性がある。

参照：[FFmpeg loudnorm](https://ffmpeg.org/ffmpeg-filters.html#loudnorm)、[yt-dlp YouTube extractor](https://github.com/yt-dlp/yt-dlp/blob/master/yt_dlp/extractor/youtube/_video.py)、[yt-dlp Niconico extractor](https://github.com/yt-dlp/yt-dlp/blob/master/yt_dlp/extractor/niconico.py)。
