package discordbot

import (
	"context"
	"sync"
	"time"
)

// Response waits retain the existing overall budget. Active I/O uses the
// existing five-second send/discovery bound; DAVE uses the remaining budget.
func voiceStageBudget(stage string) time.Duration {
	switch stage {
	case "identify", "udp_discovery", "select_protocol", "transport_key":
		return 5 * time.Second
	default:
		return voiceRecoveryTimeout
	}
}

type voiceHandshakeDeadline struct {
	mu       sync.Mutex
	stage    string
	deadline time.Time
	overall  time.Time
	timer    *time.Timer
	stopped  bool
	fail     func(string, error)
}

func (d *voiceHandshakeDeadline) advance(stage string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return false
	}
	if d.stage == stage {
		return true
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.stage = stage
	d.deadline = time.Now().Add(voiceStageBudget(stage))
	if d.overall.Before(d.deadline) {
		d.deadline = d.overall
	}
	deadline := d.deadline
	d.timer = time.AfterFunc(time.Until(deadline), func() { d.expire(stage, deadline) })
	return true
}

func (d *voiceHandshakeDeadline) current() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stage
}

func (d *voiceHandshakeDeadline) expire(stage string, deadline time.Time) {
	d.mu.Lock()
	if d.stopped || d.stage != stage || !d.deadline.Equal(deadline) {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	d.mu.Unlock()
	d.fail(stage, context.DeadlineExceeded)
}

func (d *voiceHandshakeDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

// Finish and the timer compete under the same lock, so a successful transport
// cannot be returned while a late timer is still allowed to cancel it.
func (d *voiceHandshakeDeadline) finish() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || !time.Now().Before(d.deadline) {
		return false
	}
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	return true
}
