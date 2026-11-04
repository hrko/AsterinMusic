package discordbot

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/process"
	"asterinmusic/internal/source"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

// Do not log upstream error strings: REST errors may contain interaction
// tokens, response bodies, or URLs. Only stable classifications leave here.
func failureClass(err error) string {
	var stage *playbackStageError
	if errors.As(err, &stage) {
		err = stage.err
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownMessage) {
		return "unknown_message"
	}
	for _, item := range []struct {
		err    error
		reason string
	}{
		{source.AuthRequired, "authentication_required"}, {source.AuthCookies, "cookies_unavailable"}, {source.AuthLogin, "authentication_failed"}, {source.AuthTemporary, "authentication_temporary"},
		{os.ErrPermission, "permission"}, {os.ErrNotExist, "not_found"}, {process.ErrOutputLimit, "output_limit"}, {errVoiceRecovery, "voice_recovery"}, {errVoiceRemoved, "voice_removed"}, {errSuperseded, "superseded"},
	} {
		if errors.Is(err, item.err) {
			return item.reason
		}
	}
	var ws *websocket.CloseError
	if errors.As(err, &ws) {
		return "websocket_closed"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "process_exit"
	}
	var httpErr *rest.Error
	if errors.As(err, &httpErr) {
		return "discord_api"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "upstream"
}

// Only typed, numeric diagnostics are copied. Error(), HTTP bodies, request
// URLs, executable arguments and WebSocket close text remain private.
func failureAttributes(err error) []any {
	var attrs []any
	var stage *playbackStageError
	if errors.As(err, &stage) {
		attrs = append(attrs, "stage", stage.stage)
		err = stage.err
	}
	var ws *websocket.CloseError
	if errors.As(err, &ws) {
		attrs = append(attrs, "close_code", ws.Code)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		attrs = append(attrs, "exit_code", exit.ExitCode())
	}
	var httpErr *rest.Error
	if errors.As(err, &httpErr) {
		if httpErr.Response != nil {
			attrs = append(attrs, "http_status", httpErr.Response.StatusCode)
		}
		if httpErr.Code != 0 {
			attrs = append(attrs, "discord_code", int(httpErr.Code))
		}
	}
	return attrs
}

type playbackStageError struct {
	stage string
	err   error
}

func (e *playbackStageError) Error() string { return "playback stage failed" }
func (e *playbackStageError) Unwrap() error { return e.err }

func (s *session) logOperationFailure(operation string, err error) {
	ctx := context.Background()
	if s.app != nil && s.app.ctx != nil {
		ctx = s.app.ctx
	}
	logFailure(ctx, logging.OrDiscard(s.logger), operation, err)
}

func (a *App) logOperationFailure(operation string, err error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	logger := logging.OrDiscard(a.logger)
	if a.config.GuildID != 0 {
		logger = logger.With("guild_id", a.config.GuildID.String())
	}
	logFailure(ctx, logger, operation, err)
}

func (a *App) operationLogger(id snowflake.ID) *slog.Logger {
	l := logging.Component(a.logger, "interaction")
	if a.config.GuildID != 0 {
		l = l.With("guild_id", a.config.GuildID.String())
	}
	if id != 0 {
		l = l.With("operation_id", id.String())
	}
	return l
}

func (a *App) observeOperation(id snowflake.ID, kind string) func() {
	l := a.operationLogger(id)
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	l.DebugContext(ctx, "interaction received", "event", "interaction.received", "kind", kind)
	return func() {
		l.DebugContext(ctx, "interaction handler returned", "event", "interaction.processed", "kind", kind, "duration_ms", time.Since(start).Milliseconds())
	}
}

func (a *App) logInteractionFailure(id snowflake.ID, operation string, err error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	logFailure(ctx, a.operationLogger(id), operation, err)
}

func logFailure(ctx context.Context, logger *slog.Logger, operation string, err error) {
	if err == nil {
		return
	}
	component, _, _ := strings.Cut(operation, ".")
	if component == "notification" {
		component = "interaction"
	}
	if component == "autoplay" {
		component = "playback"
	}
	logger = logging.Component(logger, component)
	if errors.Is(err, context.Canceled) {
		logger.DebugContext(ctx, "operation canceled", "event", operation+".canceled", "reason", "canceled")
		return
	}
	level := slog.LevelWarn
	action := "operation_failed"
	switch operation {
	case "history.write", "history.flush", "history.shutdown", "history.close", "history.queue_full":
		level, action = slog.LevelError, "persistence_failed"
	}
	attrs := []any{"event", operation + ".failed", "reason", failureClass(err), "action", action}
	attrs = append(attrs, failureAttributes(err)...)
	logger.Log(ctx, level, "operation failed", attrs...)
}
