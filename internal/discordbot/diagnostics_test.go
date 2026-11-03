package discordbot

import (
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
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	logOperationFailure("interaction.update_embed", errors.New("private-token https://example.com/private response-body"))
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
