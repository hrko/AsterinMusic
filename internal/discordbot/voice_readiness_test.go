package discordbot

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockedReadyTransport struct {
	*fakeVoiceTransport
	entered chan struct{}
	release chan struct{}
}

func (t *blockedReadyTransport) Ready() bool {
	close(t.entered)
	<-t.release
	return true // Deliberately stale success after the controller was closed.
}

func TestVoiceSlowReadyDoesNotBlockStopOrResurrect(t *testing.T) {
	c := testVoiceController(t)
	transport := &blockedReadyTransport{fakeVoiceTransport: newFakeVoiceTransport(), entered: make(chan struct{}), release: make(chan struct{})}
	c.active = transport
	c.resourceLease = c.resources.acquire()
	c.ownedResources = append(c.ownedResources, c.resourceLease)
	c.phase = voiceRecovering
	waited := make(chan error, 1)
	go func() { waited <- c.waitReady(context.Background()) }()
	<-transport.entered
	stopped := make(chan struct{})
	go func() { c.Close(context.Background()); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(transport.release)
		t.Fatal("readiness check held the connection state lock")
	}
	close(transport.release)
	select {
	case err := <-waited:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stale readiness resurrected closed connection: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness waiter did not finish")
	}
}
