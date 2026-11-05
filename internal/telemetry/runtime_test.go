package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPConfigurationAndCorrelation(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:1/secret")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "Authorization=environment-secret,X-Secret=environment-secret")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "gzip")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "secret=environment-secret")
	received := make(chan *collector.ExportTraceServiceRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Authorization") != "Bearer configured" || r.Header.Get("X-Secret") != "" || r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("Content-Encoding") != "" {
			t.Errorf("incorrect export configuration")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var data collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &data); err != nil {
			t.Error(err)
			return
		}
		received <- &data
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer srv.Close()
	r, err := New(context.Background(), Options{Enabled: true, Endpoint: srv.URL + "/v1/traces", Headers: map[string]string{"Authorization": "Bearer configured"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := r.Provider.Tracer("test").Start(context.Background(), "voice.recovery")
	_, child := r.Provider.Tracer("test").Start(ctx, "voice.connect")
	child.End()
	parent.End()
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-received:
		if len(data.ResourceSpans) != 1 || len(data.ResourceSpans[0].ScopeSpans[0].Spans) != 2 {
			t.Fatalf("incorrect spans: %v", data)
		}
		if strings.Contains(data.String(), "environment-secret") {
			t.Fatal("environment resource leaked")
		}
		spans := data.ResourceSpans[0].ScopeSpans[0].Spans
		if string(spans[0].TraceId) != string(spans[1].TraceId) || string(spans[0].ParentSpanId) != string(spans[1].SpanId) {
			t.Fatal("incorrect parent correlation")
		}
	default:
		t.Fatal("no export")
	}
}

func TestDisabledDoesNotRecord(t *testing.T) {
	r, err := New(context.Background(), Options{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := r.Provider.Tracer("test").Start(context.Background(), "test")
	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Fatal("disabled runtime records")
	}
	span.End()
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
}
