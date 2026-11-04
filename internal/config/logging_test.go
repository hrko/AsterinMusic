package config

import (
	"strings"
	"testing"
)

func TestLoggingConfiguration(t *testing.T) {
	c, err := Read(setup(t, valid+"logging:\n  level: warn\n  format: json\n  components:\n    voice: debug\n"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if c.Logging.Level != "warn" || c.Logging.Format != "json" || c.Logging.Components["voice"] != "debug" {
		t.Fatalf("wrong logging options: %+v", c.Logging)
	}
	c, err = Read(setup(t, valid), lookup)
	if err != nil || c.Logging.Validate() != nil {
		t.Fatalf("legacy configuration rejected: %v", err)
	}
	for _, extra := range []string{
		"  level: SECRET\n", "  format: SECRET\n", "  components:\n    SECRET: debug\n", "  components:\n    voice: SECRET\n", "  components:\n    voice: 123\n", "  unknown: SECRET\n",
	} {
		_, err := Read(setup(t, valid+"logging:\n"+extra), lookup)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("invalid or unsafe validation: %v", err)
		}
	}
}
