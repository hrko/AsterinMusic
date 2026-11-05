package discordbot

import (
	"context"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"testing"
	"time"
)

func spanString(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if a.Key == attribute.Key(key) {
			return a.Value.AsString()
		}
	}
	return ""
}
func TestVoiceTraceTimeoutKeepsStageAndIgnoresLateCompletion(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	c := testVoiceController(t)
	c.tracer = provider.Tracer("test")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.factory = func(ctx context.Context, _ voice.State, _ func(error, bool)) (voiceTransport, error) {
		traceVoiceStage(ctx, "dave")
		close(entered)
		<-release
		traceVoiceStage(ctx, "late_stage")
		close(done)
		return newFakeVoiceTransport(), nil
	}
	c.HandleVoiceStateUpdate(stateEvent(3))
	c.HandleVoiceServerUpdate(serverEvent("secret"))
	<-entered
	c.mu.Lock()
	c.deadline = time.Now().Add(-time.Second)
	c.expireLocked()
	stage := c.failureStage
	c.mu.Unlock()
	close(release)
	<-done
	if stage != "dave" {
		t.Fatalf("stage lost: %s", stage)
	}
	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("expected recovery, connect, dave: %d", len(spans))
	}
	for _, s := range spans {
		if s.Status().Code != codes.Error {
			t.Errorf("%s did not time out", s.Name())
		}
		if s.Name() == "late_stage" {
			t.Fatal("late progress recorded")
		}
	}
	if len(recorder.Started()) != len(recorder.Ended()) {
		t.Fatal("span not ended")
	}
}

func TestVoiceTraceRoundTripAndFrameExclusion(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	c := testVoiceController(t)
	c.tracer = provider.Tracer("test")
	for i, ch := range []int{3, 4, 3} {
		c.HandleVoiceStateUpdate(stateEvent(snowflake.ID(ch)))
		c.HandleVoiceServerUpdate(serverEvent(string(rune('a' + i))))
		waitVoice(t, c)
		for range 10 {
			if err := c.WriteAudio(context.Background(), []byte{1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	spans := recorder.Ended()
	if len(spans) != 6 {
		t.Fatalf("expected one recovery and connect per move, got %d", len(spans))
	}
	for _, s := range spans {
		if s.Status().Code != codes.Unset {
			t.Fatalf("successful %s failed", s.Name())
		}
		if s.Name() == "voice.connect" && !s.Parent().IsValid() {
			t.Fatal("missing recovery parent")
		}
	}
}
