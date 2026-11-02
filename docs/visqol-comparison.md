# gopus SIMDのViSQOL比較

> 検証・移行時点の記録です。現在の動作は[現行仕様](specification.md)、構成は[アーキテクチャ](architecture.md)、操作は[開発・運用手順](development.md)を参照してください。

2026-11-02。26音源を使い、libopus、gopus通常版、gopus SIMD版を比較した。今回の条件では、SIMD版に大きなスコア低下は認められなかった。左右チャンネル個別の評価を含む156条件で、SIMD版とlibopusとの差の最小値は−0.002567だった。ただし、これは聴感上の同等性を証明する試験ではない。本番Botのエンコーダーは変更していない。

## 音源と条件

[EBU SQAM](https://qc.ebu.io/testmaterials/523/)から楽器・歌唱・合奏・ポップス18音源と英語男女音声2音源、[librosaの公開音源](https://librosa.org/doc/main/recordings.html)から6音源を選んだ。バイオリン、コントラバス、フルート、トランペット、カスタネット、ドラム、シンバル、グロッケン、ピアノ、チェンバロ、アコーディオン、ソプラノ、四重唱、ギター、合唱、管弦楽、ポップス、カントリー、ドラム＆ベース、ラグタイムなどを含む。

SQAMはFLAC、librosa側は既に非可逆圧縮されたHQ Oggを使用した。後者の参照はOggを復号したPCMであり、録音原本に対する品質を測っているわけではない。SQAMはR&D用途の利用条件に従う。原音・加工音はGit管理外の`.dev`にのみ保存し、公開音源の帰属表示は[attribution](visqol-results/attribution)に保存した。

- 各音源の最初の有音区間から8秒を抽出し、前後に0.5秒ずつ無音を付けた9秒クリップ。10ms区間のRMSが0.001を超えた位置の50ms前を開始点とする。
- 入力を48kHzステレオに変換し、コーデックへ渡す前にピーク0.8へ統一。開始位置・倍率・原ファイルSHA256は[manifest](visqol-results/manifest.json)に記録。
- 48kHz、ステレオ、20msフレーム、96kbps、Audio、complexity 9、CVBR。Go 1.27.2、GOAMD64=v3。gopusは同一コミットでnosimd/simdを切り替えた。
- 音量100%とRainMusicの初期音量30%（−28dB）を試験。各出力は共通のlibopusデコーダーで復号し、遅延を補正。
- 30%条件では復号後、参照と全出力に同じ逆ゲインを掛けてからPCM16へ変換した。これにより評価用PCM16の量子化誤差を抑える。個別の出力正規化はしない。この条件が評価するのは低振幅入力の圧縮品質であり、静かなBGMを実際に聞いた際の可聴性ではない。
- ViSQOLのaudio/SVRモードを使用。L/R平均のモノラル、左、右の3通りを評価した。これはステレオ定位・空間印象の評価ではない。

26音源×2音量×3チャンネル×3エンコーダー＝468組に、同一波形156組と3kHzローパス対照26組を加え、合計650組を評価した。すべて有限値のスコアを取得できた。

## 結果

以下はL/R平均のモノラルでの平均ViSQOL MOS-LQO。ΔはSIMD−libopus。小数点以下の差は測定値の記録であり、その桁まで人間が違いを識別できることを意味しない。

| 音源群 | 件数 | 音量 | libopus | gopus通常版 | gopus SIMD | 平均Δ |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 全音源 | 26 | 100% | 4.4014 | 4.4013 | 4.4012 | −0.0001 |
| 全音源 | 26 | 30% | 4.4023 | 4.4024 | 4.4022 | 約0 |
| 非圧縮由来の音楽 | 18 | 100% | 4.3029 | 4.3030 | 4.3029 | 約0 |
| 非圧縮由来の音楽 | 18 | 30% | 4.3032 | 4.3033 | 4.3031 | 約0 |
| Ogg由来の音楽 | 6 | 100% | 4.6167 | 4.6161 | 4.6162 | −0.0005 |
| Ogg由来の音楽 | 6 | 30% | 4.6197 | 4.6198 | 4.6196 | −0.0001 |
| 英語音声 | 2 | 100% | 4.6419 | 4.6416 | 4.6418 | −0.0001 |
| 英語音声 | 2 | 30% | 4.6420 | 4.6419 | 4.6421 | ＋0.0001 |

同一波形対照は全156組が4.73210125。3kHzローパス対照は平均2.9416、範囲1.8205〜4.5334だった。スコアの上限は必ずしも5ではなく、音源によってローパス劣化への反応も異なる。

左右個別も含めた、SIMD版の相対低下が大きい上位3条件は次のとおり。

| 音源 | 音量・チャンネル | libopus | SIMD | Δ |
| --- | --- | ---: | ---: | ---: |
| Nutcracker / Kevin MacLeod | 100%・L/R平均 | 4.704485 | 4.701918 | −0.002567 |
| SQAM 13 フルート | 100%・左 | 4.649853 | 4.647639 | −0.002214 |
| Choice / AdmiralBob | 100%・L/R平均 | 4.524144 | 4.522483 | −0.001661 |

全条件の値は[summary.json](visqol-results/summary.json)、相対低下順は[comparisons.json](visqol-results/comparisons.json)、対照を含む全スコアは[ratings.json](visqol-results/ratings.json)、ViSQOL出力は[scores.csv](visqol-results/scores.csv)を参照。エンコーダーのビルド情報・遅延・パケットハッシュは[encoder](visqol-results/encoder)に保存した。

追加試聴をするなら上記3音源を優先できる。ローカルの`.dev/visqol/evaluation/<ID>/nosimd/source-100.wav`、`nosimd/libopus-100.wav`、`simd/gopus-100.wav`が同じ区間のステレオ試聴用ファイル。IDは順に`nutcracker`、`sqam13`、`choice`。

## 解釈と未検証範囲

今回の結果は、既存の試聴結果と合わせてSIMD版の採用検討を進める材料になる。一方、ViSQOLは聴感を推定する指標なので、この小さな差から聴感上の優劣や統計的同等性を断定しない。1音源につき先頭付近の8秒のみで、曲中の急変や長時間の状態変化を網羅していない。96kbps以外、パケット損失、音量変更中のランプ、Discord経由、ステレオ定位も今回の対象外。音声2件も音楽用モデルで評価しており、音声専用の品質試験を代替しない。

## 再実行

処理はGo製の`cmd/quality`に実装し、シェルスクリプトは使用していない。開発依存はmiseとロックファイルで固定した。ViSQOLのC++ビルドには別途Linuxのホストコンパイラーが必要で、今回はWSL UbuntuのGCC 13.3.0を使用した。準備と集計はWindows、ViSQOLの採点はWSL Linuxで実行した。ネイティブWindows版ViSQOLのビルドは未検証。

1. [provenance.json](visqol-results/provenance.json)のコミットでViSQOLを`.dev/visqol/source`、librosa/dataを`.dev/visqol/audio-source`へ取得する。SQAM ZIPを同ファイルのURLから取得し、SHA256を確認して`.dev/visqol/sqam`へ展開する。
2. `mise install`で依存を用意する。LinuxでmiseのPythonからvenvを作り、`experiments/visqol/requirements.txt`をインストールする。BazelへそのPythonを`PYTHON_BIN_PATH`、site-packagesを`PYTHON_LIB_PATH`で渡す。
3. ViSQOLソースのディレクトリで`bazel build :visqol -c opt --jobs=12 --cxxopt=-include --cxxopt=cstdint --host_cxxopt=-include --host_cxxopt=cstdint --copt=-w --host_copt=-w --nokeep_going`を実行する。GCC 13との互換性のためcstdintを明示的にincludeした。アルゴリズムのソース変更はしていない。
4. RainMusicで`mise run opus-simd-compare`を実行し、通常版・SIMD版の比較バイナリーを用意する。
5. `mise run quality-prepare`で26音源の参照・出力を作成する。WindowsではFFmpeg配布物のLibrary/binがPATHに必要。
6. Linuxで`mise run quality-score`を実行する。既定のViSQOLパスは`.dev/visqol/source/bazel-bin/visqol`。別の実行ファイルには`RAINMUSIC_VISQOL`を指定する。音源ごとに最大12並列で採点する。
7. `mise run quality-summary`で集計する。成果物は`.dev/visqol/evaluation`に出力される。再実行時は既存の評価成果物を上書きする。

モデル・実行ファイル・音源のハッシュ、ツールとライブラリーの固定バージョンはprovenanceとmanifestに記録済み。実装追加後の`mise run check`（整形・vet・単体テスト・Linux/Windowsクロスビルド）は成功した。
