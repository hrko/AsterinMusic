package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/source"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/disgoorg/disgo/rest"
	"github.com/gorilla/websocket"
	"log/slog"
	"net/http"
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

func TestTypedFailuresKeepCodesWithoutPrivateText(t *testing.T) {
	for _, tc := range []struct {
		err          error
		reason, code string
	}{
		{&websocket.CloseError{Code: 4006, Text: "PRIVATE"}, "websocket_closed", `"close_code":4006`},
		{&rest.Error{Response: &http.Response{StatusCode: 403}, Code: 50013, Message: "PRIVATE", RsBody: []byte("PRIVATE")}, "discord_api", `"http_status":403`},
		{fmt.Errorf("PRIVATE: %w", source.AuthCookies), "cookies_unavailable", `"reason":"cookies_unavailable"`},
	} {
		var out bytes.Buffer
		l, _ := logging.New(&out, logging.Options{Format: "json", Level: "debug"})
		logFailure(t.Context(), l, "interaction.reply", tc.err)
		if failureClass(tc.err) != tc.reason || strings.Contains(out.String(), "PRIVATE") || !strings.Contains(out.String(), tc.code) {
			t.Fatalf("unsafe or incomplete typed error: %s", out.String())
		}
	}
}

func TestSourceFailureTakesPrecedenceOverPipelineCancellation(t *testing.T) {
	err := errors.Join(context.Canceled, &playbackStageError{stage: "source", err: source.AuthRequired})
	if got := failureClass(err); got != "authentication_required" {
		t.Fatalf("source cause hidden: %s", got)
	}
	var out bytes.Buffer
	l, _ := logging.New(&out, logging.Options{Format: "json"})
	p := &playbackLog{logger: l}
	p.finish(t.Context(), slog.LevelWarn, failureClass(err), "advance", 0, err)
	if !strings.Contains(out.String(), `"stage":"source"`) {
		t.Fatalf("source stage missing: %s", out.String())
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
