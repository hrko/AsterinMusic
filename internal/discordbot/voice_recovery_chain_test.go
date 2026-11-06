package discordbot

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

// Exercise the real controller, notification deadline, rejoin and explicit-open
// policy together. Transport stage faults are injected at the factory boundary;
// local WS/UDP fixtures separately verify their wire-level classification.
func TestVoiceRecoveryChainRejoinFailureThenExplicitRetry(t *testing.T) {
	for _, stage := range []string{"websocket_connect", "hello", "ready", "udp_discovery", "select_protocol", "session_description", "transport_key", "dave"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			c := testVoiceController(t)
			c.canRejoin = func(context.Context, snowflake.ID) bool { return true }
			old := &rejectedResume{fakeVoiceTransport: newFakeVoiceTransport(), code: 4006}
			var opens, joins, leaves atomic.Int32
			c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
				switch opens.Add(1) {
				case 1:
					return old, nil
				case 2:
					return nil, &voiceStageError{stage: stage, err: context.DeadlineExceeded}
				default:
					return newFakeVoiceTransport(), nil
				}
			}
			c.update = func(_ context.Context, _ snowflake.ID, channel *snowflake.ID, _, _ bool) error {
				if channel == nil {
					leaves.Add(1)
					e := stateEvent(0)
					e.ChannelID = nil
					c.HandleVoiceStateUpdate(e)
					return nil
				}
				n := joins.Add(1)
				if n == 2 {
					return nil // No fresh notifications after Resume rejection.
				}
				// Server-before-membership is valid, including after rejoin.
				c.HandleVoiceServerUpdate(serverEvent(fmt.Sprint(n)))
				c.HandleVoiceStateUpdate(stateEvent(*channel))
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.Open(ctx, 3, false, true); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			generation := c.generation
			c.mu.Unlock()
			c.transportClosed(generation, &websocket.CloseError{Code: 1006}, true)
			for {
				c.mu.Lock()
				failed, changed := c.phase == voiceFailed, c.changed
				c.mu.Unlock()
				if failed {
					break
				}
				select {
				case <-changed:
				case <-ctx.Done():
					t.Fatal("recovery did not reach held failure")
				}
			}
			c.mu.Lock()
			failureStage, known, channel := c.failureStage, c.membershipKnown, c.confirmedChannel
			c.mu.Unlock()
			if failureStage != stage || !known || channel != 3 {
				t.Fatalf("lost failure/membership: stage=%s known=%v channel=%s", failureStage, known, channel)
			}
			if old.calls.Load() != 1 || opens.Load() != 2 || joins.Load() != 3 || leaves.Load() != 1 {
				t.Fatalf("unexpected automatic retries: resume=%d opens=%d joins=%d leaves=%d", old.calls.Load(), opens.Load(), joins.Load(), leaves.Load())
			}
			if err := c.Open(ctx, 3, false, true); err != nil {
				t.Fatalf("explicit retry after held failure: %v", err)
			}
			if opens.Load() != 3 || joins.Load() != 4 || leaves.Load() != 2 {
				t.Fatalf("explicit retry did not confirm leave/rejoin: opens=%d joins=%d leaves=%d", opens.Load(), joins.Load(), leaves.Load())
			}
			c.transportClosed(generation, &websocket.CloseError{Code: 4006}, true)
			if err := c.WriteAudio(ctx, []byte{1}); err != nil {
				t.Fatalf("new connection affected by retired generation: %v", err)
			}
		})
	}
}
