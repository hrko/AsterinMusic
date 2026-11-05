package discordbot

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"testing"
	"time"
)

func TestRestoreTraceEndsOnAudioObservation(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	defer p.Shutdown(context.Background())
	s := newSession(&App{ctx: context.Background(), tracer: p.Tracer("test")}, 1)
	s.beginRestore(&playbackCheckpoint{Playing: true})
	s.restore.prepare()
	ctx := s.restoreContext(context.Background())
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("restore context missing")
	}
	log := newPlaybackLog(ctx, nil, "test", false, s.restore)
	log.started <- playbackStart{at: time.Now(), channel: "3"}
	log.join()
	s.restore.finish("late_failure", errors.New("secret"))
	spans := rec.Ended()
	if len(spans) != 2 || spans[0].Name() != "playback.restore_prepare" || spans[0].Parent().SpanID() != spans[1].SpanContext().SpanID() || spanString(spans[1], "asterinmusic.result") != "audio_started" || spans[1].Status().Code != codes.Unset {
		t.Fatal("first audio result lost")
	}
	if trace.SpanContextFromContext(s.restoreContext(context.Background())).IsValid() {
		t.Fatal("finished restore leaked to later playback")
	}
}

func TestRestoreTraceFailureAndStoppedState(t *testing.T) {
	for _, playing := range []bool{false, true} {
		rec := tracetest.NewSpanRecorder()
		p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		s := newSession(&App{ctx: context.Background(), tracer: p.Tracer("test")}, 1)
		v := &playbackCheckpoint{Playing: playing}
		s.beginRestore(v)
		if playing {
			s.restore.finish("preparation_failed", errors.New("secret URL"))
		} else {
			if err := s.resumeCheckpoint(v); err != nil {
				t.Fatal(err)
			}
		}
		spans := rec.Ended()
		if len(spans) != 1 {
			t.Fatal("restore span unfinished")
		}
		if playing && spans[0].Status().Code != codes.Error {
			t.Fatal("preparation failure unmarked")
		}
		if !playing && spanString(spans[0], "asterinmusic.result") != "settings_restored" {
			t.Fatal("stopped restore waits for audio")
		}
		_ = p.Shutdown(context.Background())
	}
}
