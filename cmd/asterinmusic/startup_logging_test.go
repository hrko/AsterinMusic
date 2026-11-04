package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBootstrapHasOneStructuredRecord(t *testing.T) {
	var out bytes.Buffer
	reportBootstrapFailure(&out, "configuration")
	text := out.String()
	if strings.Count(text, "\n") != 1 || !strings.Contains(text, "event=app.startup.failed") || !strings.Contains(text, "stage=configuration") {
		t.Fatalf("invalid bootstrap record: %s", text)
	}
}
