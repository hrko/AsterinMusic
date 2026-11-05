package telemetry

import (
	"strings"
	"testing"
)

func TestOptions(t *testing.T) {
	for _, o := range []Options{{}, {Enabled: true, Endpoint: "http://127.0.0.1:4318/v1/traces"}, {Enabled: true, Endpoint: "https://collector.example/v1/traces", Headers: map[string]string{"Authorization": "Bearer secret"}}, {Enabled: true, Endpoint: "http://[::1]:4318/v1/traces"}} {
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range []Options{{Enabled: true}, {Endpoint: "http://collector.example/v1/traces"}, {Endpoint: "https://secret@collector.example/v1/traces"}, {Endpoint: "https://collector.example/v1/traces?token=secret"}, {Endpoint: "https://collector.example"}, {Headers: map[string]string{"Authorization": "secret\r\nInjected: value"}}, {Headers: map[string]string{"Content-Type": "secret"}}, {Headers: map[string]string{"Authorization": "secret", "authorization": "secret"}}} {
		err := o.Validate()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe validation: %v", err)
		}
	}
}
