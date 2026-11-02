package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

type gatedWriter struct {
	release <-chan struct{}
	bytes.Buffer
}

func (w *gatedWriter) Write(p []byte) (int, error) { <-w.release; return w.Buffer.Write(p) }
func TestBufferedDownloadDoesNotWaitForPlayback(t *testing.T) {
	payload := bytes.Repeat([]byte{42}, 128<<10)
	downloaded := make(chan struct{})
	release := make(chan struct{})
	c := New("fake")
	c.Run = func(_ context.Context, _ string, _ []string, _ io.Reader, w io.Writer) error {
		_, err := w.Write(payload)
		close(downloaded)
		return err
	}
	out := &gatedWriter{release: release}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { done <- c.BufferedStream(ctx, "https://www.youtube.com/watch?v=aqz-KE-bpKQ", out) }()
	select {
	case <-downloaded:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("download blocked on playback")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, out.Bytes()) {
		t.Fatal("buffer content changed")
	}
}
func TestBufferedCancellation(t *testing.T) {
	c := New("fake")
	c.Run = func(ctx context.Context, _ string, _ []string, _ io.Reader, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.BufferedStream(ctx, "https://www.youtube.com/watch?v=aqz-KE-bpKQ", io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestSpoolLimitAndError(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "spool")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := &spool{file: f, ctx: context.Background(), changed: make(chan struct{}), limit: 3}
	if _, err = b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Write([]byte("d")); err == nil {
		t.Fatal("limit exceeded")
	}
	sentinel := errors.New("download failure")
	b.finish(sentinel)
	got, err := io.ReadAll(b)
	if string(got) != "abc" || !errors.Is(err, sentinel) {
		t.Fatal(string(got), err)
	}
}
