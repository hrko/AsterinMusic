package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func native(action string) error {
	return nativeTarget(action, "")
}

func nativeTarget(action, target string) error {
	if action == "dave-check" {
		return pureDave(action, target)
	}
	if action == "build-voice" || action == "bot-start" || action == "bot-restart" || action == "voice-check" || action == "dave-check" {
		backend := os.Getenv("RAINMUSIC_DAVE_BACKEND")
		if backend == "" || backend == "go" {
			return pureDave(action, target)
		}
		if backend != "native" {
			return fmt.Errorf("RAINMUSIC_DAVE_BACKEND must be go or native")
		}
	}
	if action == "native-audio-check" {
		return command([]string{"CGO_ENABLED=0", "GOEXPERIMENT=simd", "GOAMD64=v3", "RAINMUSIC_AUDIO_TEST=1"}, "go", "test", "-tags", "opus", "./internal/audio", "-count=1", "-v")
	}
	out, err := exec.Command("mise", "where", "github:discord/libdave").Output()
	if err != nil {
		return fmt.Errorf("locate libdave: %w", err)
	}
	root := strings.TrimSpace(string(out))
	out, err = exec.Command("mise", "where", "ffmpeg").Output()
	if err != nil {
		return fmt.Errorf("locate opus: %w", err)
	}
	opusRoot := strings.TrimSpace(string(out))
	if runtime.GOOS == "windows" {
		opusRoot = filepath.Join(opusRoot, "Library")
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	dir, err := filepath.Abs("bin")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	helper := filepath.Join(dir, "rainmusic-pkgconfig"+ext)
	if err = command([]string{"CGO_ENABLED=0"}, "go", "build", "-o", helper, "./cmd/pkgconfig"); err != nil {
		return err
	}
	// cgo caches pkg-config output. Include its implementation and root in the
	// compile flags so tool/path changes invalidate that cache.
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		return err
	}
	signature := sha256.Sum256(append(helperBytes, []byte(root+opusRoot)...))
	env := []string{"CGO_ENABLED=1", "CC=zig cc", "CXX=zig c++", "PKG_CONFIG=" + helper, "RAINMUSIC_DAVE_ROOT=" + root, "PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "LD_LIBRARY_PATH=" + filepath.Join(root, "lib") + string(os.PathListSeparator) + os.Getenv("LD_LIBRARY_PATH")}
	env = append(env, fmt.Sprintf("CGO_CFLAGS=-O2 -DRAINMUSIC_PKGCONFIG_%x=1", signature[:8]))
	env = append(env, "RAINMUSIC_OPUS_ROOT="+opusRoot,
		"PATH="+filepath.Join(opusRoot, "bin")+string(os.PathListSeparator)+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LD_LIBRARY_PATH="+filepath.Join(opusRoot, "lib")+string(os.PathListSeparator)+filepath.Join(root, "lib")+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	if action == "opus-simd-compare" {
		return compareSIMD(env, dir, ext)
	}
	if action == "dave-interop" {
		return command(env, "go", "test", "-tags", "daveinterop", "./internal/davecheck", "-count=1", "-v")
	}
	if action == "opus-compare" {
		if err := command(env, "go", "-C", "experiments/opusbench", "test", "-v", "./..."); err != nil {
			return err
		}
		output, err := filepath.Abs(filepath.Join(".dev", "opusbench", runtime.GOOS))
		if err != nil {
			return err
		}
		args := []string{"-C", "experiments/opusbench", "run", ".", "-out", output}
		if input := os.Getenv("RAINMUSIC_OPUS_PCM"); input != "" {
			input, err = filepath.Abs(input)
			if err != nil {
				return err
			}
			args = []string{"-C", "experiments/opusbench", "run", ".", "-out", filepath.Join(output, filepath.Base(input)), "-pcm", input}
		}
		return command(env, "go", args...)
	}
	if action == "race-test" {
		env = append(env, "GOEXPERIMENT=simd", "GOAMD64=v3")
		if runtime.GOOS == "windows" {
			env = append(env, "CGO_LDFLAGS=-lapi-ms-win-core-synch-l1-2-0")
		}
		return command(env, "go", "test", "-tags", "davego,opus", "-race", "./...")
	}
	binary := filepath.Join(dir, "rainmusic-voice"+ext)
	if target != "" {
		binary = target
	}
	env = append(env, "GOEXPERIMENT=simd", "GOAMD64=v3")
	if err = command(env, "go", "build", "-tags", "dave,opus", "-o", binary, "./cmd/rainmusic"); err != nil {
		return err
	}
	if action == "build-voice" {
		return nil
	}
	if action == "bot-restart" {
		if err := command(env, binary, append([]string{"discord-check"}, launchArgs...)...); err != nil {
			return fmt.Errorf("preflight failed; current bot retained: %w", err)
		}
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		if err = stopBot(root); err != nil {
			return err
		}
	}
	if action == "bot-start" || action == "bot-restart" {
		env = append(env, fmt.Sprintf("RAINMUSIC_RESUME=%t", action == "bot-restart"))
		return launchBot(env, binary)
	}
	return command(env, binary, append([]string{action}, launchArgs...)...)
}
