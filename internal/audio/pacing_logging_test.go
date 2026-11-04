//go:build opus

package audio

import (
	"asterinmusic/internal/logging"
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestPacingLogThreshold(t *testing.T) {
	for _, tc := range []struct {
		frames, late int
		want         slog.Level
	}{{0, 0, slog.LevelDebug}, {100, 1, slog.LevelDebug}, {1000, 49, slog.LevelDebug}, {1000, 50, slog.LevelWarn}, {10000, 50, slog.LevelDebug}} {
		if got := pacingLevel(tc.frames, tc.late); got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}

func TestFrameLoopLogsOnlySummary(t *testing.T) {
	var out bytes.Buffer
	l, err := logging.New(&out, logging.Options{Format: "json", Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	err = encodeFrames(t.Context(), bytes.NewReader(make([]byte, 960*2*4*3)), NewVolume(48000), 0, func([]float32) ([]byte, error) { return []byte{0}, nil }, func([]byte) error { frames++; return nil }, logging.Component(l, "playback"))
	if err != nil {
		t.Fatal(err)
	}
	if frames != 3 || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), `"frames":3`) {
		t.Fatalf("unexpected frame logging: %s", out.String())
	}
}
