# 音声接続改善の検証状況

[改善計画](voice-handshake-plan.md)の完了確認用の記録。テストの存在だけを完了の根拠とせず、注入した障害と観測した結果を区別する。現時点では計画全体の完了を宣言していない。

## 検証対象と証拠

以下のファイルは`internal/discordbot/`にある。実装の現行仕様は[アーキテクチャ](architecture.md)を参照する。

| 計画の検証対象 | 現在の証拠 | 残る確認範囲 |
| --- | --- | --- |
| 段階別失敗 | `voice_trace_fault_test.go`はローカルWSの応答保留・切断、UDP探索失敗、暗号方式不一致、鍵設定失敗、DAVE準備保留を注入し、段階記録を検査する。`voice_handshake_deadline_test.go`は期限・旧タイマー・成功との排他を検査する。Identify・SelectProtocolの送信中断は下記27組で検査する | 個別I/O期限と全体期限の対応を最終照合する |
| 1006→4006→再入室→失敗→明示再開 | `voice_recovery_chain_test.go`は実接続管理に切断とResume拒否を与え、通知待ち・退出確認・再入室を経て8段階のfactory失敗を注入し、新しい明示試行を検査する | 段階失敗はfactory境界での注入。実WS・UDPの段階検査と組み合わせた証拠であり、全行程を実ソケットだけで再現したものではない |
| Resume | `voice_reconnect_test.go`と`voice_socket_test.go`に、成立後のDAVE待ち、再試行予算、4006拒否、Heartbeat、Resume前後のソケット処理の検査がある。`TestVoiceTransportHandshakeAndFreshResources`はローカルWSでResume前後のSpeaking受信、同一UDPの送信2回・Open1回、VCごとのDAVE生成を検査する。`TestVoiceUDPRTPEncryptionAndResumeContinuity`は実UDPでRTP番号・タイムスタンプと暗号化を検査する | UDP単体の連続送信検査とTransportのResume検査は別々のfixture。実Discordでの可聴性は別途確認する |
| 停止・移動・外部退出 | `voice_handshake_interrupt_test.go`はWS接続・Hello・Identify・Ready・UDP探索・SelectProtocol・SessionDescription・鍵設定・DAVEの9段階で3操作を実行し、旧資源の解放、旧世代の拒否、移動先の送信を検査する。送信2段階は実TLS接続を包んだnet.Connで対象WSフレームのWriteを保留し、Closeで解除する。書き込み開始の同期点を待ってから操作する | カーネル内の送信停滞を再現する検査ではなく、ソケット境界での制御可能な障害注入 |
| 通知順序・重複 | `voice_description_test.go`は同値鍵応答の再初期化防止、異なる鍵応答とプロトコル選択前の応答の拒否、DAVE外部送信者通知の先行を検査する | 外部送信者テストはルーティングの検査であり、MLS暗号処理の証明ではない |
| 実I/O・資源解放 | `voice_udp_test.go`の実UDP中断、`voice_resources_test.go`の実終了までの枠保持、`voice_transport_test.go`のイベント溢れと停止したワーカーの追跡。上記27組ではfactory・ソケット・ワーカーの終了まで資源枠が完了しないことを検査する。DAVE監視の実終了は[専用記録](dave-shutdown-verification.md)に分離 | 実Discordでの可聴性は別途確認する |
| 手動再開と要求結果 | `resume_request_test.go`は最初の音声送信まで成功を返さず、連打・停止・移動・空キュー・音源失敗・接続失敗を区別する。連続音源失敗は3曲で結果を確定し、残りの1曲と接続を保持したまま再開できることを追加検査。`TestResumeQueueClearPreservesOnlyHeldCurrentTrack`は受付後に実際の確認付きキュー消去を行い、待機曲だけなら対象なし、保持中の現在曲があればその曲だけ再開することを検査する。`TestResumeRejectsMovementDuringAuthorization`は認可中の利用者移動・Botとの同時移動を注入し、再開要求を作らずキューを保持することを検査する | 各期限境界を既存テストと最終照合 |
| Discord応答更新失敗 | `resume_response_test.go`は実コマンドハンドラーの受付・結果更新にHTTP 403を返し、それでも最初の音声送信と再生が続くことを検査する | 実Discordへの障害注入は行っていない |
| 所属と追加受付 | `voice_membership_test.go`、`queue_connection_test.go`、`voice_connection_test.go`に確認済み所属と目標の分離、世代・二重開始の検査がある。上記9段階×3操作の中断fixtureは、操作前に実際の`acceptAt`で通常・優先追加を行い、同じ接続世代と試行が保持され、新たな再生ループを開始しないことも検査する。準備中の上限・別VCからの追加拒否も別テストで確認する | 実サイトの取得処理を含めた各URL種別の再試験はしていない。共通受付以降の契約を対象としている |
| 計測と秘密情報 | `voice_diagnostics_test.go`、`voice_logging_test.go`、`voice_trace_fault_test.go`に遅い出力・バッファ溢れ・鍵等を含まない診断の検査がある。`TestHandshakeFailureSummaryWithoutTracingOrDebug`はトレース無効・INFO設定でfactory失敗を発生させ、ERROR要約の識別子・期限・段階・通知受信・次の判断をJSONとして検査する | factory境界で進行記録を作る検査と実WSの段階検査を組み合わせている。リモート側の内部原因は判定対象外 |
| ビルド・race | `38a57d9`までの`mise run check`とWSLの`mise run race-test`が成功。Windows/Linux、通常・本番タグを含む | 追加変更に応じた関連検査 |
| 実Discord | 管理再起動後の単一プロセス・ready・ローカル音声送信開始は確認済み | 最新版の可聴性、VC往復、停止後のURLなし`/play`は利用者確認待ち。過去版の成功や送信ログで代替しない |

## 完了判定

「残る確認範囲」の未確認事項を解消し、実機確認を含む計画の完了条件を満たしてから完了とする。段階テストの不足を、同じ処理を通るという推測だけで完了扱いにしない。保持曲・キュー順・音量・自動再生・履歴・管理保存は既存の回帰テストも維持する。
