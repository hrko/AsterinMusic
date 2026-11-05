package main

import (
	"asterinmusic/internal/telemetry"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// checkTraces sends synthetic spans only. It never reads Bot credentials.
func checkTraces() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := telemetry.New(ctx, telemetry.Options{Enabled: true, Endpoint: "http://127.0.0.1:4318/v1/traces"}, "local-check")
	if err != nil {
		return err
	}
	childCtx, parent := r.Provider.Tracer("asterinmusic/dev").Start(ctx, "diagnostic.local")
	_, child := r.Provider.Tracer("asterinmusic/dev").Start(childCtx, "diagnostic.child")
	child.End()
	parent.End()
	if err := r.Shutdown(); err != nil {
		return err
	}
	id := parent.SpanContext().TraceID().String()
	client := &http.Client{Timeout: time.Second}
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:4319/api/traces/"+id, nil)
		resp, err := client.Do(req)
		if err == nil {
			var result struct {
				Data []struct {
					TraceID string            `json:"traceID"`
					Spans   []json.RawMessage `json:"spans"`
				} `json:"data"`
			}
			decodeErr := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&result)
			resp.Body.Close()
			if decodeErr == nil && len(result.Data) == 1 && result.Data[0].TraceID == id && len(result.Data[0].Spans) == 2 {
				fmt.Printf("OTLP round trip verified: http://127.0.0.1:4319/trace/%s\n", id)
				return nil
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("local Jaeger did not return the exported trace; start mise run traces first")
}
