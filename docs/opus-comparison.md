# Pure Go Opus比較（2026-11-02）

> 検証・移行時点の記録です。現在の動作は[現行仕様](specification.md)、構成は[アーキテクチャ](architecture.md)、操作は[開発・運用手順](development.md)を参照してください。

この文書はGo 1.26.1・gopus v0.1.2での初回比較記録。現在の比較モジュールはGo 1.27.2とSIMD対応コミットへ更新している。追加検証は[SIMD比較](opus-simd-comparison.md)を参照。

今回の結果では、Pure Go化の次の検証対象としてgopus v0.1.2を推奨する。全候補が復号・音量応答の検査を通過し、20msの処理枠に収まった。gopusはPionより概ね高速で、今回の音源における波形誤差がlibopusに近い。ただし聴感上の同等性や本番運用の安定性は、この測定だけでは判断できない。稼働Botは現行libopusのままとした。

## 条件

- CPU：AMD RYZEN AI MAX+ 395（16コア・32論理プロセッサ）。Windows amd64と同一機のWSL Ubuntu amd64、Go 1.26.1。
- 基準：mise管理FFmpegに付属するlibopus 1.6.1。RainMusicと同じApplication Audio・96kbps、その他は既定値。
- Pion：`v0.1.1-0.20261026214112-e7b8aa55508c`。gopus：`v0.1.2`。独立した`experiments/opusbench/go.mod`・`go.sum`で固定し、Botの依存には追加しない。
- 48kHz・ステレオ・20ms、Pure Go側はcomplexity 9・constrained VBR・96kbpsを明示。名称が同じ設定でも各実装の分析・符号化判断は一致しない。
- 合成音12秒：左右で異なる和音、チャープ、打撃音、ノイズ、1秒の無音。加えて以前取得したYouTube・ニコニコ音声の先頭12秒をWindowsで評価。
- 各音源を原音ゲイン1.0とRainMusicの音量30%相当（振幅約0.03981、−28dB）で処理。入力PCMのSHA256をJSONへ保存。
- 各条件で100フレームのウォームアップ後、1,800フレームの時間測定と、別の1,800フレームのGoヒープ割り当て測定を実施。生成・初期化・デコード・ファイル出力は測定時間から除く。
- WindowsはQueryPerformanceCounter、LinuxはGoの単調時計を使用。Windowsの通常時計で分解能不足が見られたため変更した。時計呼び出しの割り当てを除くため、時間と割り当ては別ループで測定する。
- 復号はすべて同じlibopusで行い、各パケットから960サンプル／チャンネルを復元できることを確認。WAVを比較試聴用に保存する。

## 合成音・原音ゲインの結果

| OS | エンコーダー | 平均／20msフレーム | p99 | 遅延補正後SNR |
| --- | --- | ---: | ---: | ---: |
| Windows | libopus | 86.5µs | 138.0µs | 22.68dB |
| Windows | Pion | 175.4µs | 301.2µs | 20.85dB |
| Windows | gopus | 128.6µs | 280.8µs | 22.82dB |
| WSL Linux | libopus | 87.2µs | 200.2µs | 22.68dB |
| WSL Linux | Pion | 179.9µs | 320.0µs | 20.85dB |
| WSL Linux | gopus | 135.9µs | 240.1µs | 22.82dB |

gopusの平均はWindowsでlibopusの約1.49倍、Pionは約2.03倍。ただしgopusでも20msの約0.64%であり、このPCで単一エンコーダーを動かす限り十分な余裕がある。複数サーバー同時再生の容量を保証する数値ではない。

全測定条件で20ms超過は0回。Goヒープ割り当ては小数点以下2桁で0.00回／フレーム（プロセス全体のMemStats差分なので微小なランタイム由来の差分を含み得る）。C側のメモリ割り当てやプロセスRSSはこの指標に含めない。

## 実音源・原音ゲインの結果（Windows）

| 音源 | エンコーダー | 平均 | p99 | 遅延補正後SNR |
| --- | --- | ---: | ---: | ---: |
| YouTubeサンプル | libopus | 94.8µs | 125.8µs | 18.25dB |
| YouTubeサンプル | Pion | 192.1µs | 364.0µs | 18.13dB |
| YouTubeサンプル | gopus | 168.7µs | 408.9µs | 18.25dB |
| ニコニコサンプル | libopus | 92.3µs | 132.0µs | 16.39dB |
| ニコニコサンプル | Pion | 172.7µs | 338.2µs | 16.36dB |
| ニコニコサンプル | gopus | 130.9µs | 202.0µs | 16.36dB |

音量30%相当でも全候補が処理を完了した。SNRはYouTubeが18.02〜18.11dB、ニコニコが16.33〜16.37dB。原音ゲインの合成音で検出された遅延はlibopus・gopusが312サンプル（6.5ms）、Pionが120サンプル（2.5ms）。これは波形の相関から測った値であり、アプリの送信・ネットワーク・Discordクライアントの遅延を含まない。

SNRは左右両チャンネルの遅延を補正し、ゲイン補正を加えず原音との差から計算した波形指標である。非可逆圧縮の聴感品質を順位付けする指標ではなく、gopusのSNRが少し高いことを「libopusより高音質」と解釈してはいけない。測定器には既知の遅延・振幅半減に対する回帰テストを付けた。

## 音量応答・互換性

440Hzの音にRainMusicと同じ10msのゲインランプを適用し、1.0→0→1.0を各エンコーダーに入力した。両OS・全候補で、復号音のミュート（基準RMSから−60dB以下）と復帰（基準の80%以上）は、いずれも入力変更後40ms以内だった。これはパケット内の音声時間であり、実時間のDiscord送信試験ではない。

`CGO_ENABLED=0`で候補エンコーダーを実行する専用タスクを用意し、Windows・Linuxの両方で成功した。比較タスク自体は基準libopusと共通デコーダーのためにCGOを使う。CGO無効タスクはエンコードのみの検査で、JSONの`EncodeOnly=true`の場合、SNRなどの復号指標の0は未測定を表す。

## 再実行と成果物

```text
mise run opus-compare
mise run opus-pure-check
```

`opus-compare`は指標・音量応答のテストと合成音比較を実行し、`.dev/opusbench/<OS>/results.json`と試聴用WAVを出力する。`RAINMUSIC_OPUS_PCM`に48kHz・ステレオ・float32 little-endianのファイルパスを設定すると、先頭12秒を比較し、ファイル名別のディレクトリに出力する。短い末尾フレームは切り捨てる。

実音源は`.dev/youtube-sample.webm`と`.dev/nico-verified.bin`からFFmpegで上記PCMへ変換したものを使用した。音声自体はGitへ含めず、計測JSONは`docs/opus-results/`に保存した。通常の`mise run check`は独立モジュールを対象にしないため、変更時には比較タスクも実行する。

## 採用判断と残る確認

gopus v0.1.2を次の統合検証候補とする。現時点で本番経路の置き換えは行わない。次に必要なのは、複数ジャンルでの音量を揃えたブラインド試聴、長時間・複数同時ストリーム、CPU負荷下の期限超過、Discord上での停止・スキップ・再接続の確認である。Pionも今回の機能検査は通過しており、代替候補として残せる。

計測は通常のデスクトップ環境で行い、CPU固定・他プロセス隔離・統計的な反復試験は行っていない。短い音源の同一実行内反復であり、最大遅延・音質・メモリの長期保証ではない。外部プロセスのデコード時間、音量処理、ネットワーク送信は速度比較から除外している。

実装資料：[Pion固定コミット](https://github.com/pion/opus/tree/e7b8aa55508c)、[gopus v0.1.2](https://github.com/thesyncim/gopus/tree/v0.1.2)、[libopus API](https://opus-codec.org/docs/opus_api-1.6/group__opus__encoder.html)。
