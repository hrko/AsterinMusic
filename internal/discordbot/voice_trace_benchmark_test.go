package discordbot

import (
	"context"
	"testing"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type readyTraceDave struct{ godave.Session }

func (readyTraceDave) Ready() bool { return true }

type frameTraceConn struct {
	voice.Conn
	udp *protocolTestUDP
}

func (c frameTraceConn) UDP() voice.UDPConn                                   { return c.udp }
func (frameTraceConn) SetSpeaking(context.Context, voice.SpeakingFlags) error { return nil }

func traceFrameFixture(tracer trace.Tracer) *voiceConnection {
	c := newVoiceConnection(1, 2)
	c.tracer = tracer
	c.phase = voiceReady
	c.active = &discordVoiceTransport{tracer: tracer, dave: readyTraceDave{}, conn: frameTraceConn{udp: &protocolTestUDP{}}, sendGate: make(chan struct{}, 1)}
	return c
}

func TestVoiceReadyFramesDoNotCreateSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	c := traceFrameFixture(provider.Tracer("test"))
	for range 10000 {
		if err := c.WriteAudio(context.Background(), []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.Started()) != 1 || len(recorder.Ended()) != 1 || recorder.Ended()[0].Name() != "voice.speaking" {
		t.Fatal("frame-level instrumentation or unfinished Speaking span")
	}
}

// Compare the real controller + transport send path. UDP and DAVE are fixtures:
// this isolates instrumentation overhead, not network jitter or codec cost.
func BenchmarkVoiceReadyFrameTracing(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			var provider trace.TracerProvider = noop.NewTracerProvider()
			if enabled {
				p := sdktrace.NewTracerProvider()
				defer p.Shutdown(context.Background())
				provider = p
			}
			c := traceFrameFixture(provider.Tracer("benchmark"))
			ctx := context.Background()
			packet := make([]byte, 240)
			if err := c.WriteAudio(ctx, packet); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := c.WriteAudio(ctx, packet); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
