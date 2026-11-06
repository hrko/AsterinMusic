package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type stalledOutput struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	bytes.Buffer
}

func (w *stalledOutput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.Buffer.Write(p)
}

func TestAsyncOutputBoundsStalledWriterAndReportsOverflow(t *testing.T) {
	w := &stalledOutput{entered: make(chan struct{}), release: make(chan struct{})}
	base, _ := New(w, Options{Format: "json"})
	logger, output := Async(base)
	logger.Info("first", "event", "first")
	<-w.entered
	const count = 2000
	for i := 0; i < count; i++ {
		logger.Info("fixture", "event", fmt.Sprintf("event.%d", i))
	}
	output.mu.Lock()
	size, categories := len(output.queue), len(output.overflow)
	output.mu.Unlock()
	if size > asyncCapacity || categories > asyncOverflowCapacity+1 {
		t.Fatal("unbounded output buffer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output.Close(ctx) == nil {
		t.Fatal("blocked output reported drained")
	}
	close(w.release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := output.Close(ctx); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(w.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		total++
		if n, ok := record["output_coalesced"].(float64); ok {
			total += int(n)
		}
	}
	if total != count+1 {
		t.Fatalf("lost overflow accounting: %d", total)
	}
}

func TestAsyncOutputRetainsComponentFilteringAndAttributes(t *testing.T) {
	var buffer bytes.Buffer
	base, _ := New(&buffer, Options{Format: "json", Level: "warn", Components: map[string]string{"voice": "debug"}})
	logger, output := Async(base)
	Component(logger, "voice").With("guild_id", "fixture").Debug("voice", "event", "voice.fixture")
	Component(logger, "source").Info("filtered", "event", "source.fixture")
	logger.WithGroup("nested").Error("group", slog.String("value", "kept"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := output.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if text := buffer.String(); !strings.Contains(text, `"guild_id":"fixture"`) || !strings.Contains(text, `"nested":{"value":"kept"}`) || strings.Contains(text, "filtered") {
		t.Fatal(text)
	}
}

func TestAsyncOutputRetainsTraceAndRedaction(t *testing.T) {
	var buffer bytes.Buffer
	base, _ := New(&buffer, Options{Format: "json"})
	logger, output := Async(base)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	logger.InfoContext(ctx, "fixture", "token", "synthetic-secret", "event", "fixture")
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := output.Close(deadline); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != sc.TraceID().String() || strings.Contains(buffer.String(), "synthetic-secret") {
		t.Fatal("lost correlation or redaction")
	}
}
