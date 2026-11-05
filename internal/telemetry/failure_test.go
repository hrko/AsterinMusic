package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestFullQueueDropsWithoutBlockingProducer(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var data collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &data); err != nil {
			t.Error(err)
			return
		}
		for _, resource := range data.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				received.Add(int64(len(scope.Spans)))
			}
		}
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	r, err := New(context.Background(), Options{Enabled: true, Endpoint: srv.URL + "/v1/traces"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	tracer := r.Provider.Tracer("test")
	for range 128 {
		_, span := tracer.Start(context.Background(), "voice.connect")
		span.End()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not start")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 6000 {
			_, span := tracer.Start(context.Background(), "voice.connect")
			span.End()
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("span producer blocked behind export")
	}
	close(release)
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	// One in-flight batch plus the bounded queue, not all 6128 spans.
	if n := received.Load(); n < 128 || n > 128+1024 {
		t.Fatalf("unexpected retained span count: %d", n)
	}
}

func TestShutdownDeadlineCancelsRemainingNetworkWork(t *testing.T) {
	var requests, active atomic.Int64
	entered := make(chan struct{}, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	r, err := New(context.Background(), Options{Enabled: true, Endpoint: srv.URL + "/v1/traces"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	tracer := r.Provider.Tracer("test")
	for range 1200 {
		_, span := tracer.Start(context.Background(), "voice.connect")
		span.End()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not start")
	}
	start := time.Now()
	if err := r.Shutdown(); err == nil {
		t.Fatal("expected incomplete shutdown")
	}
	if elapsed := time.Since(start); elapsed > ShutdownTimeout+time.Second {
		t.Fatalf("shutdown exceeded bound: %s", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("network work survived shutdown")
	}
	before := requests.Load()
	time.Sleep(100 * time.Millisecond)
	if requests.Load() != before {
		t.Fatal("queued export started after shutdown")
	}
}

func TestUnavailableCollectorDoesNotPreventStartup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := srv.URL + "/v1/traces"
	srv.Close()
	r, err := New(context.Background(), Options{Enabled: true, Endpoint: endpoint}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := r.Provider.Tracer("test").Start(context.Background(), "voice.connect")
	span.End()
	start := time.Now()
	_ = r.Shutdown() // Delivery is best effort; the application can still stop.
	if time.Since(start) > ShutdownTimeout+time.Second {
		t.Fatal("unavailable collector prevented shutdown")
	}
}

func TestDiagnosticsSuppressRawErrorsAndRateLimit(t *testing.T) {
	previous := otel.GetErrorHandler()
	defer otel.SetErrorHandler(previous)
	var output bytes.Buffer
	ConfigureDiagnostics(slog.New(slog.NewJSONHandler(&output, nil)))
	for range 100 {
		otel.Handle(errors.New("https://user:synthetic-secret@example.invalid/token?key=synthetic-secret"))
	}
	if strings.Contains(output.String(), "synthetic-secret") {
		t.Fatal("raw SDK error leaked")
	}
	if strings.Count(output.String(), "tracing.diagnostic.failed") != 1 {
		t.Fatal("SDK diagnostics were not rate limited")
	}
}
