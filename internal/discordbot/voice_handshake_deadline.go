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
	init     sync.Once
	record   *voiceTraceAttempt
	deadline time.Time
	overall  time.Time
	timer    *time.Timer
	stopped  bool
	fail     func(string, error)
}

func (d *voiceHandshakeDeadline) progressRecord() *voiceTraceAttempt {
	d.init.Do(func() {
		if d.record == nil {
			d.record = &voiceTraceAttempt{ctx: context.Background(), tracer: tracerOrNoop(nil)}
		}
	})
	return d.record
}

func (d *voiceHandshakeDeadline) advance(stage string) bool {
	r := d.progressRecord()
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.stopped || r.ended {
		return false
	}
	if r.name == stage && !d.deadline.IsZero() {
		return true
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	r.progressLocked(stage)
	d.deadline = time.Now().Add(voiceStageBudget(stage))
	if d.overall.Before(d.deadline) {
		d.deadline = d.overall
	}
	r.stageDeadline = d.deadline
	r.deadlineScope = "stage"
	if d.deadline.Equal(d.overall) {
		r.deadlineScope = "recovery"
	}
	deadline := d.deadline
	d.timer = time.AfterFunc(time.Until(deadline), func() { d.expire(stage, deadline) })
	return true
}

func (d *voiceHandshakeDeadline) current() string {
	return d.progressRecord().current()
}

func (d *voiceHandshakeDeadline) expire(stage string, deadline time.Time) {
	r := d.progressRecord()
	r.mu.Lock()
	if d.stopped || r.ended || r.name != stage || !d.deadline.Equal(deadline) {
		r.mu.Unlock()
		return
	}
	d.stopped = true
	r.mu.Unlock()
	d.fail(stage, context.DeadlineExceeded)
}

func (d *voiceHandshakeDeadline) stop() {
	r := d.progressRecord()
	r.mu.Lock()
	defer r.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

// Finish and the timer compete under the same lock, so a successful transport
// cannot be returned while a late timer is still allowed to cancel it.
func (d *voiceHandshakeDeadline) finish() bool {
	r := d.progressRecord()
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.stopped || r.ended || !time.Now().Before(d.deadline) {
		return false
	}
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	return true
}
