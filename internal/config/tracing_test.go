package config

import "testing"

func TestTracingConfiguration(t *testing.T) {
	c, err := Read(setup(t, valid+"tracing:\n  enabled: true\n  endpoint: http://127.0.0.1:4318/v1/traces\n  headers:\n    Authorization: Bearer example\n"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Tracing.Enabled || c.Tracing.Headers["Authorization"] != "Bearer example" {
		t.Fatal("configuration not resolved")
	}
	if _, err := Read(setup(t, valid+"tracing:\n  enabled: true\n"), lookup); err == nil {
		t.Fatal("missing endpoint accepted")
	}
}
