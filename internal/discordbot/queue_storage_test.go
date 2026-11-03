package discordbot

import (
	"context"
	"testing"
	"time"
)

func TestQueueStorageWaitAllowsStopAndStatus(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.waitQueueLocked()
		s.queueIOLocked(func() { close(entered); <-release })
		close(finished)
	}()
	<-entered
	responsive := make(chan struct{})
	go func() { s.stop(); s.playbackStatus(); close(responsive) }()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("storage blocked stop/status")
	}
	second := make(chan struct{})
	go func() { s.mu.Lock(); defer s.mu.Unlock(); s.waitQueueLocked(); close(second) }()
	select {
	case <-second:
		t.Fatal("second mutation bypassed pending save")
	default:
	}
	close(release)
	<-finished
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("save waiter not released")
	}
}
