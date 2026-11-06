package discordbot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func TestFailedExplicitOpenUsesConfirmedMembership(t *testing.T) {
	for _, mode := range []string{"joined", "left", "cached"} {
		t.Run(mode, func(t *testing.T) {
			c := testVoiceController(t)
			c.canRejoin = func(context.Context, snowflake.ID) bool { return true }
			c.phase, c.channel = voiceFailed, 3
			if mode == "cached" {
				c.lookupMembership = func() (snowflake.ID, bool) { return 3, true }
			} else {
				e := stateEvent(3)
				if mode == "left" {
					e.ChannelID = nil
				}
				c.HandleVoiceStateUpdate(e)
			}
			var mu sync.Mutex
			var requests []string
			c.update = func(_ context.Context, _ snowflake.ID, channel *snowflake.ID, _, _ bool) error {
				mu.Lock()
				if channel == nil {
					requests = append(requests, "leave")
				} else {
					requests = append(requests, "join")
				}
				mu.Unlock()
				if channel == nil {
					e := stateEvent(0)
					e.ChannelID = nil
					c.HandleVoiceStateUpdate(e)
				} else {
					c.HandleVoiceStateUpdate(stateEvent(*channel))
					c.HandleVoiceServerUpdate(serverEvent("fresh"))
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := c.Open(ctx, 3, false, true); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if mode == "left" {
				if len(requests) != 1 || requests[0] != "join" {
					t.Fatalf("unnecessary leave: %v", requests)
				}
			} else if len(requests) != 2 || requests[0] != "leave" || requests[1] != "join" {
				t.Fatalf("expected confirmed leave before join: %v", requests)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if !c.membershipKnown || c.confirmedChannel != 3 || c.server != nil {
				t.Fatal("confirmed membership lost when notification consumed")
			}
		})
	}
}

func TestUnknownRecoveryMembershipDoesNotGuessFromTarget(t *testing.T) {
	c := testVoiceController(t)
	f := recoveryFixture(t, c)
	f.resetMembership = true
	c.lookupMembership = func() (snowflake.ID, bool) { return 0, false }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	f.ctx, f.cancel = ctx, cancel
	var requests int
	c.update = func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { requests++; return nil }
	c.runRecovery(f, false, true)
	if requests != 0 || !c.Failed() {
		t.Fatalf("unconfirmed membership sent %d requests", requests)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failureStage != "membership_confirmation" || c.membershipKnown {
		t.Fatal("unknown membership was treated as joined or left")
	}
}

func TestTargetAndConfirmedMembershipRemainSeparate(t *testing.T) {
	c := testVoiceController(t)
	c.phase = voiceFailed
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.mu.Lock()
	c.channel = 4 // A later requested target does not confirm its membership.
	c.discardLocked()
	if !c.membershipKnown || c.confirmedChannel != 3 {
		t.Fatal("discard lost confirmed membership")
	}
	c.mu.Unlock()
	e := stateEvent(0)
	e.ChannelID = nil
	c.HandleVoiceStateUpdate(e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.membershipKnown || c.confirmedChannel != 0 {
		t.Fatal("leave was not confirmed")
	}
}
