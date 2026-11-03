// dev is the cross-platform implementation of mise tasks. No shell scripts.
package main

import (
	"asterinmusic/internal/config"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(env []string, name string, args ...string) error {
	fmt.Println(name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var launchArgs []string
var explicitConfig bool
var restartAfterTrack bool

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: dev check|fmt|build|audio-check")
	}
	var taskArgs []string
	for _, arg := range os.Args[2:] {
		if arg == "--after-track" {
			if os.Args[1] != "bot-restart" || restartAfterTrack {
				return errors.New("--after-track is only valid once for bot-restart")
			}
			restartAfterTrack = true
		} else {
			taskArgs = append(taskArgs, arg)
		}
	}
	o, rest, err := config.ExtractOptions(taskArgs)
	if err != nil {
		return err
	}
	explicitConfig = len(taskArgs) > 0
	launchArgs = append(rest, o.Args()...)
	if len(rest) > 0 && os.Args[1] != "voice-check" {
		return errors.New("unexpected task argument")
	}
	switch os.Args[1] {
	case "serve":
		return manage("start")
	case "bot-start", "bot-stop", "bot-restart", "bot-status", "bot-logs":
		return manage(strings.TrimPrefix(os.Args[1], "bot-"))
	case "voice-check":
		return exclusiveVoiceCheck()
	case "opus-pure-check":
		if err := command([]string{"CGO_ENABLED=0"}, "go", "-C", "experiments/opusbench", "test", "./..."); err != nil {
			return err
		}
		output, err := filepath.Abs(filepath.Join(".dev", "opusbench", runtime.GOOS+"-pure"))
		if err != nil {
			return err
		}
		return command([]string{"CGO_ENABLED=0"}, "go", "-C", "experiments/opusbench", "run", ".", "-pure", "-out", output)
	case "build-voice", "dave-check":
		return pureDave(os.Args[1], "")
	case "opus-audio-check":
		return command([]string{"CGO_ENABLED=0", "GOEXPERIMENT=simd", "GOAMD64=v3", "ASTERINMUSIC_AUDIO_TEST=1"}, "go", "test", "-tags", "opus", "./internal/audio", "-count=1", "-v")
	case "race-test":
		env := []string{"CGO_ENABLED=1", "CC=zig cc", "CXX=zig c++", "GOEXPERIMENT=simd", "GOAMD64=v3"}
		if runtime.GOOS == "windows" {
			env = append(env, "CGO_LDFLAGS=-lapi-ms-win-core-synch-l1-2-0")
		}
		return command(env, "go", "test", "-tags", "davego,opus", "-race", "./...")
	case "opus-compare", "opus-simd-compare", "dave-interop":
		return comparison(os.Args[1])
	case "fmt", "check":
		var files []string
		err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == ".dev" || d.Name() == "bin") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if os.Args[1] == "fmt" {
			return command(nil, "gofmt", append([]string{"-w"}, files...)...)
		}
		cmd := exec.Command("gofmt", append([]string{"-l"}, files...)...)
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(out)) > 0 {
			return fmt.Errorf("run mise run fmt:\n%s", out)
		}
		if err := command(nil, "go", "vet", "./..."); err != nil {
			return err
		}
		if err := command(nil, "go", "test", "./..."); err != nil {
			return err
		}
		productionEnv := []string{"CGO_ENABLED=0", "GOEXPERIMENT=simd", "GOAMD64=v3"}
		for _, action := range []string{"vet", "test"} {
			if err := command(productionEnv, "go", action, "-tags", "davego,opus", "./..."); err != nil {
				return err
			}
		}
		for _, target := range []string{"linux", "windows"} {
			if err := command([]string{"GOOS=" + target, "GOARCH=amd64", "CGO_ENABLED=0"}, "go", "build", "./..."); err != nil {
				return err
			}
			if err := command([]string{"GOOS=" + target, "GOARCH=amd64", "CGO_ENABLED=0", "GOEXPERIMENT=simd", "GOAMD64=v3"}, "go", "build", "-tags", "davego,opus", "./..."); err != nil {
				return err
			}
		}
		return nil
	case "build":
		if err := os.MkdirAll("bin", 0755); err != nil {
			return err
		}
		name := "asterinmusic"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return command(nil, "go", "build", "-o", filepath.Join("bin", name), "./cmd/asterinmusic")
	case "audio-check":
		return command([]string{"ASTERINMUSIC_AUDIO_TEST=1"}, "go", "test", "./internal/audio", "-run", "Integration", "-count=1", "-v")
	default:
		return errors.New("unknown task")
	}
}
