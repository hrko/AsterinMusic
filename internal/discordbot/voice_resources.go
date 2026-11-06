package discordbot

import (
	"context"
	"sync"
	"time"
)

// A single configured Bot needs one active transport. Eight slots allow
// overlapping moves while bounding stuck factories/cleanup across controllers.
const voiceResourceCapacity = 8
const voiceCleanupWarningAfter = 5 * time.Second

type voiceResources struct {
	mu     sync.Mutex
	leases []*voiceResourceLease
}

type voiceResourceLease struct {
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	timer    *time.Timer
	watching bool
	note     func(string, time.Duration)
}

func (r *voiceResources) acquire() *voiceResourceLease {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.leases[:0]
	for _, lease := range r.leases {
		select {
		case <-lease.done:
		default:
			kept = append(kept, lease)
		}
	}
	r.leases = kept
	if len(kept) >= voiceResourceCapacity {
		return nil
	}
	lease := &voiceResourceLease{done: make(chan struct{})}
	r.leases = append(r.leases, lease)
	return lease
}

// A timeout is diagnostic only. The resource slot remains occupied until its
// actual factory/Close completes; repeated requests cannot hide or multiply it.
func (l *voiceResourceLease) watch() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.watching {
		return
	}
	select {
	case <-l.done:
		return
	default:
	}
	l.watching = true
	l.timer = time.AfterFunc(voiceCleanupWarningAfter, func() {
		select {
		case <-l.done:
			return
		default:
		}
		if l.note != nil {
			l.note("pending", voiceCleanupWarningAfter)
		}
	})
}

func (l *voiceResourceLease) retire(t voiceTransport) {
	l.once.Do(func() {
		if t == nil {
			l.finish()
			return
		}
		t.Invalidate()
		l.watch()
		go func() {
			started := time.Now()
			t.Close()
			if l.note != nil {
				l.note("completed", time.Since(started))
			}
			l.finish()
		}()
	})
}

func (l *voiceResourceLease) finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
	}
	close(l.done)
}

type voiceResourceKey struct{}

func disposeVoiceTransport(ctx context.Context, t voiceTransport) {
	if lease, ok := ctx.Value(voiceResourceKey{}).(*voiceResourceLease); ok {
		lease.retire(t)
	} else {
		t.Close()
	}
}
