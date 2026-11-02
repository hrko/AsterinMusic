# gopusのSIMD比較（2026-11-02）

> 検証・移行時点の記録です。現在の動作は[現行仕様](specification.md)、構成は[アーキテクチャ](architecture.md)、操作は[開発・運用手順](development.md)を参照してください。

今回の条件では、Go 1.27の実験的SIMDを有効にするとgopusのエンコード時間が約35〜39%短縮した。通常版・SIMD版とも全パケットをlibopusで復号でき、音量応答のテストを通過した。通常版とSIMD版のパケットは完全一致ではない。聴感上の同等性を確認した結果ではないため、稼働Botは現行libopusのままとした。

## 更新と比較条件

- プロジェクトのGoを1.27.2へ更新。`mise.toml`、両OSの`mise.lock`、ルートと比較用モジュールの`go.mod`へ反映した。
- gopusは`v0.2.3-0.20261023192543-88e87250952e`に固定。前回のv0.1.2とは異なるため、前回の性能値をSIMDの効果の算出には使わない。
- CPU：AMD RYZEN AI MAX+ 395。Windows amd64、同じPCのWSL Ubuntu amd64。
- 両版とも`GOAMD64=v3`、`GOMAXPROCS=1`、`-pgo=off`。`GOEXPERIMENT=nosimd`と`GOEXPERIMENT=simd`だけを切り替える。gopus配布のPGOプロファイルは適用しない。
- 48kHz・ステレオ・20ms・96kbps・complexity 9・constrained VBR。初期音量30%相当の−28dBと、原音ゲイン1.0を測定。
- 各実行で100フレームのウォームアップ後、1,800フレームを計測。割り当ては時計の影響を除く別ループで測定。両版を先にビルドし、通常→SIMD、SIMD→通常、通常→SIMDの順で3回ずつ実行する。
- 合成音12秒を両OSで測定。既存のYouTube・ニコニコ音声の先頭12秒はWindowsで測定。入力SHA256は前回と同じ。
- 基準のlibopus 1.6.1も各実行で測定。これは現行配布ライブラリとの実用上の比較であり、CとGoの全命令・最適化設定を一致させる比較ではない。

## 性能結果

表は原音ゲイン1.0での3回の中央値。「平均の中央値」と「各回のp99の中央値」であり、全サンプルをまとめて再計算したp99ではない。

| OS・音源 | 通常版の平均 | SIMD版の平均 | 時間短縮 | 通常版p99 | SIMD版p99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Windows・合成音 | 117.3µs | 71.1µs | 39.4% | 230.8µs | 161.3µs |
| Linux・合成音 | 118.0µs | 73.8µs | 37.4% | 228.9µs | 156.1µs |
| Windows・YouTube | 128.1µs | 78.8µs | 38.5% | 261.3µs | 168.3µs |
| Windows・ニコニコ | 112.9µs | 73.4µs | 35.1% | 196.5µs | 152.9µs |

初期音量30%相当の合成音でも、Windowsは111.9→69.6µs、Linuxは113.5→71.8µsへ短縮した。SIMD版の平均は20msの処理枠の約0.35〜0.40%に相当する。

全実行・全条件で20ms超過は0回。最大観測時間は約4.14msであり、平均の改善が最大遅延の保証になるわけではない。Goヒープ割り当ては最大でも約0.0045回／フレームで、小数点以下2桁では0.00。プロセス全体のMemStats差分なのでランタイム由来の微小な割り当てを含み得る。

## 復号・音量応答・SIMDの確認

生成した全パケットを共通のlibopusデコーダーで復号し、20msあたり960サンプル／チャンネルになることを確認した。10msのゲインランプを使ったミュート・復帰のテストは、両OS・両版とも音声時間40ms以内だった。ネットワークやDiscordクライアントの再生遅延は含めない。

| 原音ゲインの音源 | 通常版SNR | SIMD版SNR |
| --- | ---: | ---: |
| 合成音 | 22.69dB | 22.67dB |
| YouTube | 18.26dB | 18.25dB |
| ニコニコ | 16.38dB | 16.39dB |

SNRは遅延補正後の波形誤差であり、知覚音質のスコアではない。パケット列のSHA256は通常版とSIMD版で異なるが、それぞれの版では3回の実行で再現した。合成音のパケット列は同じ版でWindowsとLinux間でも一致した。丸め・演算順序などによる違いは考えられるが、ハッシュだけから原因を断定していない。

SIMD版では実行時の`archsimd.X86.AVX()`と`AVX2()`が両OSでtrueになり、ビルド情報に`GOEXPERIMENT=simd`と`GOAMD64=v3`が記録された。`go list`でもgopusのCELT用SIMDソースが選択されたことを保存した。カーネルごとの呼び出し回数を追跡するプロファイルは今回採取していない。

`CGO_ENABLED=0`でもSIMD版gopusをビルド・実行できた。比較本体は基準libopusと復号検査のためにCGOを使用する。Goの`purego`ビルドタグはgopusのSIMDを無効化するため、CGO無効の確認にはこのタグを指定しない。

## 再実行

```text
mise run opus-simd-compare
```

このタスクはamd64 v3対応CPU向け。標準のBotビルドには`GOAMD64=v3`や`GOEXPERIMENT=simd`を設定していない。SIMDのAPIはGoの実験的機能なので、再現には固定したGoとgopusの組み合わせを使う。

結果は`.dev/opusbench/simd-<OS>/synthetic/{nosimd,simd}-<1〜3>/results.json`へ出力する。`RAINMUSIC_OPUS_PCM`に48kHz・ステレオ・float32 little-endianのファイルパスを指定すると、同じ手順で実音源を測定する。入力ファイル名が結果ディレクトリ名になる。比較用WAVはこのタスクでは生成しない。

今回の記録は`docs/opus-results/simd-windows/`と`simd-linux/`に保存。`simd-summary.json`は各条件の中央値・最大値・パケットハッシュの集計。`*-celt-files.json`に選択されたソース一覧を保存した。旧比較結果のJSONは保持している。

## 判断

Go更新後のWindowsの`mise run check`と`native-audio-check`、Linuxの競合検出テスト、両OSのDAVE・Opus対応ネイティブビルドが成功した。WindowsのBotは稼働中の実行ファイルと別名でビルドして確認し、Botの再起動や音声経路の切り替えは行っていない。

このCPUではSIMDを有効にする効果が確認できたため、gopusを統合検証するなら、この固定コミットのSIMD版が有力。ただし現時点では実験タスクだけに適用する。低性能CPUや非AVX2環境、arm64、長時間の多重再生、CPU高負荷時、ブラインド試聴、Discord上の実再生は今回の検証範囲外。3回の交互実行は順序の影響を減らすためのもので、信頼区間を伴う統計的な性能保証ではない。

参考：[Go 1.27のSIMD](https://go.dev/doc/go1.27#simd)、[gopus固定コミットのビルド条件](https://github.com/thesyncim/gopus/tree/88e87250952e#build-options)。
