package main

import (
	"asterinmusic/internal/logging"
	"errors"
	"io"
)

var errAlreadyLogged = errors.New("operation failed; see structured log")

// Bootstrap has no trusted runtime configuration yet. Never print filesystem,
// control-server or upstream error strings here.
func reportBootstrapFailure(out io.Writer, stage string) {
	logger, _ := logging.New(out, logging.Options{})
	logging.Component(logger, "app").Error("application startup failed", "event", "app.startup.failed", "stage", stage)
}
