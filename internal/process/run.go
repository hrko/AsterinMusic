// Package process runs bounded, cancellable subprocesses without a shell.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Run's writers must not block indefinitely. stderr is bounded internally.
func Run(ctx context.Context, executable string, args []string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	stderr := &tailBuffer{limit: 16 * 1024}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	configure(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w: %s", executable, err, stderr.data)
	}
	return nil
}

type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return n, nil
	}
	if excess := len(b.data) + len(p) - b.limit; excess > 0 {
		b.data = b.data[excess:]
	}
	b.data = append(b.data, p...)
	return n, nil
}

var ErrOutputLimit = errors.New("subprocess output exceeds limit")

type LimitedWriter struct {
	Writer    io.Writer
	Remaining int64
}

func (w *LimitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, ErrOutputLimit
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, err
}
