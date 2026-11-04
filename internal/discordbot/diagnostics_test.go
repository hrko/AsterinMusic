package discordbot

import (
	"asterinmusic/internal/logging"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestOperationLogDoesNotExposeUpstreamDetails(t *testing.T) {
	var output bytes.Buffer
	logger, err := logging.New(&output, logging.Options{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	logFailure(t.Context(), logger, "interaction.update_embed", errors.New("private-token https://example.com/private response-body"))
	got := output.String()
	if strings.Contains(got, "private") || strings.Contains(got, "response-body") || !strings.Contains(got, "interaction.update_embed") || !strings.Contains(got, "upstream") {
		t.Fatal("unsafe or missing diagnostic classification")
	}
	if failureClass(fmt.Errorf("private: %w", context.DeadlineExceeded)) != "timeout" {
		t.Fatal("wrapped timeout not classified")
	}
	if failureClass(fmt.Errorf("private: %w", context.Canceled)) != "canceled" {
		t.Fatal("wrapped cancellation not classified")
	}
}

func TestOperationFailureLevels(t *testing.T) {
	for _, tc := range []struct {
		operation string
		err       error
		level     string
	}{
		{"interaction.reply", context.Canceled, "DEBUG"},
		{"interaction.reply", context.DeadlineExceeded, "WARN"},
		{"history.write", errors.New("secret"), "ERROR"},
		{"history.flush", errors.New("secret"), "ERROR"},
		{"history.snapshot", errors.New("secret"), "WARN"},
	} {
		var out bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
		logFailure(t.Context(), logger, tc.operation, tc.err)
		if !strings.Contains(out.String(), `"level":"`+tc.level+`"`) || strings.Contains(out.String(), "secret") {
			t.Fatalf("wrong classification: %s", out.String())
		}
	}
}
