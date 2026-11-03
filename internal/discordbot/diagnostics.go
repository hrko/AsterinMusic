package discordbot

import (
	"context"
	"errors"
	"log/slog"
	"net"

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

func logOperationFailure(operation string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		slog.Debug("operation canceled", "operation", operation)
		return
	}
	slog.Warn("operation failed", "operation", operation, "reason", failureClass(err))
}
