package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a native fixture, so tests require no shell,
// yt-dlp installation, network, or Discord credentials.
func TestHelper(t *testing.T) {
	if os.Getenv("ASTERINMUSIC_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "sleep":
		time.Sleep(time.Minute)
	case "fail":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 32*1024))
		os.Exit(7)
	case "output":
		fmt.Print(strings.Repeat("x", 1024))
	default:
		fmt.Print(os.Args[len(os.Args)-1])
	}
	os.Exit(0)
}

func TestLiteralArgument(t *testing.T) {
	t.Setenv("ASTERINMUSIC_HELPER", "1")
	var out bytes.Buffer
	arg := "spaces & $() ; 日本語"
	err := Run(context.Background(), os.Args[0], []string{"-test.run=^TestHelper$", "--", arg}, nil, &out)
	if err != nil || out.String() != arg {
		t.Fatalf("%q %v", out.String(), err)
	}
}

func TestCancellation(t *testing.T) {
	t.Setenv("ASTERINMUSIC_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Run(ctx, os.Args[0], []string{"-test.run=^TestHelper$", "--", "sleep"}, nil, &bytes.Buffer{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancellation too slow")
	}
}

func TestOutputAndErrorBounds(t *testing.T) {
	t.Setenv("ASTERINMUSIC_HELPER", "1")
	err := Run(context.Background(), os.Args[0], []string{"-test.run=^TestHelper$", "--", "fail"}, nil, &bytes.Buffer{})
	if err == nil || len(err.Error()) > 18*1024 {
		t.Fatalf("error not bounded: %v", err)
	}
	var out bytes.Buffer
	err = Run(context.Background(), os.Args[0], []string{"-test.run=^TestHelper$", "--", "output"}, nil, &LimitedWriter{Writer: &out, Remaining: 100})
	if err == nil || out.Len() > 100 {
		t.Fatal("limit not enforced")
	}
}
