package discordbot

import (
	"context"
	"reflect"
	"testing"
)

func TestVoiceProgressPreservesStageAndTerminalSnapshot(t *testing.T) {
	tracer := tracerOrNoop(nil)
	ctx, span := tracer.Start(context.Background(), "voice.connect")
	a := &voiceTraceAttempt{ctx: ctx, span: span, tracer: tracer}
	ctx = context.WithValue(ctx, voiceTraceKey{}, a)
	a.progress("hello")
	started := a.stageStarted
	a.progress("hello")
	if !a.stageStarted.Equal(started) {
		t.Fatal("duplicate stage reset duration")
	}
	a.progress("udp_discovery")
	if a.lastCompleted != "hello" {
		t.Fatal("last completed stage lost")
	}
	traceVoiceReceived(ctx, 2)
	traceVoiceEvent(ctx, 2, true)
	a.end(context.DeadlineExceeded)
	want := a.diagnostic()
	traceVoiceReceived(ctx, 6)
	traceVoiceEvent(ctx, 2, false)
	a.progress("dave")
	if got := a.diagnostic(); !reflect.DeepEqual(got, want) {
		t.Fatalf("late events changed terminal diagnostic: before=%v after=%v", want, got)
	}
}
