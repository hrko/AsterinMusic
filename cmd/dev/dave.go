package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func pureDave(action, target string) error {
	env := []string{"CGO_ENABLED=0", "GOEXPERIMENT=simd", "GOAMD64=v3"}
	if action == "dave-check" {
		if err := command(env, "go", "test", "-tags", "davego,opus", "./...", "-count=1"); err != nil {
			return err
		}
		return command(env, "go", "test", "github.com/thomas-vilte/dave-go/...", "-count=1")
	}
	if target == "" {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		var err error
		target, err = filepath.Abs(filepath.Join("bin", "rainmusic-voice"+ext))
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	fmt.Println("DAVE backend: pure Go (CGO disabled)")
	if err := command(env, "go", "build", "-tags", "davego,opus", "-o", target, "./cmd/rainmusic"); err != nil {
		return err
	}
	if action == "build-voice" {
		return nil
	}
	if action == "bot-restart" {
		if err := command(env, target, append([]string{"discord-check"}, launchArgs...)...); err != nil {
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
		return launchBot(env, target)
	}
	return command(env, target, append([]string{action}, launchArgs...)...)
}
