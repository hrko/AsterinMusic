package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceCorrelation(t *testing.T) {
	for _, sampled := range []bool{false, true} {
		var out bytes.Buffer
		l, err := New(&out, Options{Format: "json"})
		if err != nil {
			t.Fatal(err)
		}
		flags := trace.TraceFlags(0)
		if sampled {
			flags = trace.FlagsSampled
		}
		sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)
		l.With("trace_id", "stale").WithGroup("operation").InfoContext(ctx, "test", "token", "secret")
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["trace_id"] != sc.TraceID().String() || got["span_id"] != sc.SpanID().String() || got["trace_sampled"] != sampled || bytes.Contains(out.Bytes(), []byte("secret")) {
			t.Fatalf("bad correlation: %s", out.Bytes())
		}
		out.Reset()
		l.With("trace_id", "stale").Info("no context")
		if bytes.Contains(out.Bytes(), []byte("trace_id")) {
			t.Fatal("stale trace retained")
		}
	}
}
