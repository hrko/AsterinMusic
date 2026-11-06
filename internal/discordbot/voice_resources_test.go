package discordbot

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
)

func TestVoiceResourceLimitSharedAcrossControllers(t *testing.T) {
	var pool voiceResources
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, voiceResourceCapacity+1)
	for i := 0; i <= voiceResourceCapacity; i++ {
		c := testVoiceController(t)
		c.resources = &pool
		c.factory = func(context.Context, voice.State, func(error, bool)) (voiceTransport, error) {
			started <- struct{}{}
			<-release
			return newFakeVoiceTransport(), nil
		}
		c.HandleVoiceStateUpdate(stateEvent(3))
		c.HandleVoiceServerUpdate(serverEvent("credential"))
		if i < voiceResourceCapacity {
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("factory not started")
			}
		} else {
			c.mu.Lock()
			failed := c.phase == voiceFailed && c.failureStage == "cleanup_backlog" && c.failureReason == "resource_limit"
			c.mu.Unlock()
			if !failed {
				t.Fatal("exhausted resource pool did not fail explicitly")
			}
			select {
			case <-started:
				t.Fatal("factory started beyond shared resource limit")
			default:
			}
		}
	}
}

func TestVoiceCloseDeadlineDoesNotReleasePendingResource(t *testing.T) {
	c := testVoiceController(t)
	transport := &delayedCloseTransport{fakeVoiceTransport: newFakeVoiceTransport(), release: make(chan struct{}), entered: make(chan struct{})}
	lease := c.resources.acquire()
	c.active, c.resourceLease = transport, lease
	c.ownedResources = append(c.ownedResources, lease)
	defer func() {
		close(transport.release)
		select {
		case <-lease.done:
		case <-time.After(time.Second):
			t.Error("resource never released")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Close(ctx)
	<-transport.entered
	select {
	case <-lease.done:
		t.Fatal("deadline treated as cleanup completion")
	default:
	}
	if !transport.invalid.Load() {
		t.Fatal("closed controller still permits sending")
	}
}

type delayedCloseTransport struct {
	*fakeVoiceTransport
	release chan struct{}
	entered chan struct{}
	calls   atomic.Int32
}

func (t *delayedCloseTransport) Close() {
	t.calls.Add(1)
	close(t.entered)
	<-t.release
	t.fakeVoiceTransport.Close()
}

func TestVoiceResourcesRetainSlotUntilActualCleanup(t *testing.T) {
	var pool voiceResources
	lease := pool.acquire()
	transport := &delayedCloseTransport{fakeVoiceTransport: newFakeVoiceTransport(), release: make(chan struct{}), entered: make(chan struct{})}
	defer close(transport.release)
	lease.retire(transport)
	lease.retire(transport)
	<-transport.entered
	if !transport.invalid.Load() {
		t.Fatal("closing transport can still send")
	}
	for i := 1; i < voiceResourceCapacity; i++ {
		if pool.acquire() == nil {
			t.Fatal("unrelated connection blocked by cleanup")
		}
	}
	if pool.acquire() != nil {
		t.Fatal("resource limit bypassed while Close is pending")
	}
	select {
	case <-lease.done:
		t.Fatal("cleanup reported complete before Close returned")
	default:
	}
	if transport.calls.Load() != 1 {
		t.Fatal("duplicate cleanup")
	}
}

func TestVoiceResourcesReleaseCompletedFactory(t *testing.T) {
	var pool voiceResources
	leases := make([]*voiceResourceLease, voiceResourceCapacity)
	for i := range leases {
		leases[i] = pool.acquire()
	}
	if pool.acquire() != nil {
		t.Fatal("unbounded factories")
	}
	leases[0].retire(nil)
	leases[0].retire(nil)
	if pool.acquire() == nil {
		t.Fatal("completed factory still occupies slot")
	}
}

func TestVoiceResourcesWaitForCleanupCompletion(t *testing.T) {
	var pool voiceResources
	lease := pool.acquire()
	transport := newFakeVoiceTransport()
	lease.retire(transport)
	select {
	case <-lease.done:
	case <-time.After(time.Second):
		t.Fatal("cleanup completion not published")
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("slot released before transport closed")
	}
}
