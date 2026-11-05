package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const ShutdownTimeout = 3 * time.Second

// ConfigureDiagnostics is called once by the executable before initializing
// the SDK. No external error text reaches the process-wide OTel handler.
func ConfigureDiagnostics(logger *slog.Logger) {
	var last atomic.Int64
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		now := time.Now().Unix()
		prev := last.Load()
		if now-prev < 60 || !last.CompareAndSwap(prev, now) {
			return
		}
		logger.Warn("tracing diagnostic failure", "event", "tracing.diagnostic.failed", "component", "app")
	}))
}

type Runtime struct {
	Provider  trace.TracerProvider
	sdk       *sdktrace.TracerProvider
	transport *http.Transport
}

func New(ctx context.Context, o Options, version string) (*Runtime, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	r := &Runtime{Provider: noop.NewTracerProvider()}
	if !o.Enabled {
		return r, nil
	}
	// Dedicated client: do not inherit proxy, TLS, redirect or tracing settings.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(o.Endpoint),
		otlptracehttp.WithHeaders(maps.Clone(o.Headers)),
		otlptracehttp.WithTLSClientConfig(nil),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf),
		otlptracehttp.WithTimeout(2*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
		otlptracehttp.WithMaxRequestSize(4<<20), otlptracehttp.WithMaxResponseSize(64<<10),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("tracing exporter initialization failed")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		_ = exporter.Shutdown(ctx)
		transport.CloseIdleConnections()
		return nil, errors.New("tracing instance ID initialization failed")
	}
	res := resource.NewSchemaless(attribute.String("service.name", "asterinmusic"), attribute.String("service.version", version), attribute.String("service.instance.id", hex.EncodeToString(id[:])))
	r.sdk = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithoutPanicRecording(),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{AttributeValueLengthLimit: 256, AttributeCountLimit: 32, EventCountLimit: 32, LinkCountLimit: 4, AttributePerEventCountLimit: 8, AttributePerLinkCountLimit: 4}),
		sdktrace.WithBatcher(safeExporter{SpanExporter: exporter, resource: res}, sdktrace.WithMaxQueueSize(1024), sdktrace.WithMaxExportBatchSize(128), sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(2*time.Second)),
	)
	r.Provider = r.sdk
	r.transport = transport
	return r, nil
}

func (r *Runtime) Shutdown() error {
	if r.sdk == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	defer r.transport.CloseIdleConnections()
	if err := r.sdk.Shutdown(ctx); err != nil {
		return errors.New("tracing shutdown incomplete")
	}
	return nil
}

// Exporter errors can contain credentials, URLs or response bodies. The SDK
// error handler must never receive the original error.
type safeExporter struct {
	sdktrace.SpanExporter
	resource *resource.Resource
}

type exportSpan struct {
	sdktrace.ReadOnlySpan
	resource *resource.Resource
}

func (s exportSpan) Resource() *resource.Resource { return s.resource }

func (s safeExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	// SDK WithResource also merges OTEL_RESOURCE_ATTRIBUTES. Replace the
	// exported resource with the application's explicit allowlist.
	clean := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		clean[i] = exportSpan{ReadOnlySpan: span, resource: s.resource}
	}
	if err := s.SpanExporter.ExportSpans(ctx, clean); err != nil {
		return errors.New("tracing export failed")
	}
	return nil
}
func (s safeExporter) Shutdown(ctx context.Context) error {
	if err := s.SpanExporter.Shutdown(ctx); err != nil {
		return errors.New("tracing exporter shutdown failed")
	}
	return nil
}
