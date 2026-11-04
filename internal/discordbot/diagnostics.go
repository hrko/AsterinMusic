package discordbot

import (
	"asterinmusic/internal/logging"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/disgoorg/disgo/rest"
)

// Do not log upstream error strings: REST errors may contain interaction
// tokens, response bodies, or URLs. Only stable classifications leave here.
func failureClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownMessage) {
		return "unknown_message"
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
	logger.Log(ctx, level, "operation failed", "event", operation+".failed", "reason", failureClass(err), "action", action)
}
