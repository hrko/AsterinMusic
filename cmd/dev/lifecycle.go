package main

import (
	"asterinmusic/internal/config"
	"asterinmusic/internal/runcontrol"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"
)

func manage(action string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// Read-only diagnostics remain available while a restart owns the guard.
	if action == "logs" {
		return showLogs(root)
	}
	if action == "status" {
		state, running, err := botState(root)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("running PID=%d ready=%t executable=%s\n", state.PID, state.Ready, state.Executable)
			if state.Playback != nil {
				fmt.Printf("playback active=%t started=%t sequence=%d\n", state.Playback.Active, state.Playback.Started, state.Playback.Sequence)
			}
		} else {
			fmt.Println("stopped")
		}
		return nil
	}
	guard, err := net.Listen("tcp", runcontrol.Address(root, true))
	if err != nil {
		return fmt.Errorf("another lifecycle operation is running (or control port conflict): %w", err)
	}
	defer guard.Close()
	state, running, err := botState(root)
	if err != nil {
		return err
	}
	switch action {
	case "stop":
		return stopBot(root)
	case "start":
		if running {
			fmt.Printf("already running PID=%d ready=%t; no second process started\n", state.PID, state.Ready)
			return nil
		}
	case "restart":
		if running && !explicitConfig && len(state.Arguments) > 0 {
			o, rest, e := config.ExtractOptions(state.Arguments)
			if e != nil || len(rest) != 1 || rest[0] != "serve" {
				return errors.New("invalid managed launch arguments")
			}
			launchArgs = o.Args()
		}
	default:
		return errors.New("unknown lifecycle operation")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	target := filepath.Join(root, "bin", fmt.Sprintf("asterinmusic-voice-%d%s", time.Now().UnixNano(), suffix))
	return pureDave("bot-"+action, target)
}

func waitRestartBoundary(root string) error {
	if !restartAfterTrack {
		return nil
	}
	state, running, err := botState(root)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	if state.Playback == nil {
		return errors.New("running Bot does not support --after-track; perform a normal restart to upgrade first")
	}
	if !state.Playback.Active {
		fmt.Println("no active playback; restarting now")
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	fmt.Println("build and preflight complete; waiting for next track's first audio packet (Ctrl+C cancels without stopping Bot)")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("restart cancelled; current Bot retained")
		case <-ticker.C:
			next, err := runcontrol.Request(root, "status")
			if err != nil {
				return fmt.Errorf("cannot observe playback; refusing restart: %w", err)
			}
			if next.PID != state.PID || next.Executable != state.Executable || next.Playback == nil {
				return errors.New("Bot identity or playback protocol changed; refusing restart")
			}
			if boundaryReached(*state.Playback, *next.Playback) {
				fmt.Println("track boundary or playback stop observed; restarting")
				return nil
			}
		}
	}
}

func boundaryReached(before, after runcontrol.Playback) bool {
	return !after.Active || (after.Started && after.Sequence > before.Sequence)
}

func exclusiveVoiceCheck() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	guard, err := net.Listen("tcp", runcontrol.Address(root, true))
	if err != nil {
		return err
	}
	defer guard.Close()
	if _, running, err := botState(root); err != nil {
		return err
	} else if running {
		return errors.New("stop the Bot with mise run bot-stop before voice-check")
	}
	return pureDave("voice-check", "")
}

func botState(root string) (runcontrol.State, bool, error) {
	pids, err := runningBots(root)
	if err != nil {
		return runcontrol.State{}, false, fmt.Errorf("process inspection failed; refusing operation: %w", err)
	}
	state, controlErr := runcontrol.Request(root, "status")
	if controlErr == nil {
		if len(pids) != 1 || pids[0] != state.PID {
			return state, true, fmt.Errorf("Bot/control process mismatch: PIDs=%v; refusing operation", pids)
		}
		return state, true, nil
	}
	if len(pids) > 0 {
		return state, true, fmt.Errorf("unmanaged or unresponsive Bot PIDs=%v; refusing to start or kill processes", pids)
	}
	// Also verify the lock is free. Failure is not evidence of a stopped Bot.
	probe, err := net.Listen("tcp", runcontrol.Address(root, false))
	if err != nil {
		return state, false, fmt.Errorf("control endpoint unavailable but lock occupied; refusing operation: %w", err)
	}
	probe.Close()
	return state, false, nil
}

func stopBot(root string) error {
	state, running, err := botState(root)
	if err != nil {
		return err
	}
	if !running {
		fmt.Println("already stopped")
		return nil
	}
	if _, err = runcontrol.Request(root, "stop"); err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		pids, e := runningBots(root)
		if e != nil {
			// Windows can deny image-path queries while a process is exiting.
			// Retry inspection; never interpret that error as process absence.
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if len(pids) == 0 {
			fmt.Printf("stopped PID=%d; queue checkpoint retained\n", state.PID)
			return nil
		}
		if len(pids) != 1 || pids[0] != state.PID {
			return fmt.Errorf("unexpected processes during stop: %v", pids)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("graceful stop timed out; no forced termination or replacement performed")
}

func launchBot(env []string, binary string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, running, e := botState(root); e != nil {
		return e
	} else if running {
		return errors.New("Bot started during build; refusing duplicate")
	}
	dir := filepath.Join(root, ".dev", "logs")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, fmt.Sprintf("bot-%d.log", time.Now().UnixNano()))
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err = os.MkdirAll(filepath.Join(root, ".dev", "run"), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, ".dev", "run", "latest-log"), []byte(logPath), 0600); err != nil {
		return err
	}
	cmd := exec.Command(binary, append([]string{"serve"}, launchArgs...)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = root
	cmd.Stdout = log
	cmd.Stderr = log
	detached(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("Bot exited before ready (%v); see %s", err, logPath)
		case <-deadline.C:
			return fmt.Errorf("Bot readiness timed out (PID=%d); inspect bot-status/bot-logs before retrying; log=%s", pid, logPath)
		case <-ticker.C:
			state, err := runcontrol.Request(root, "status")
			if err == nil && state.PID == pid && state.Ready {
				fmt.Printf("ready PID=%d executable=%s\nlog=%s\n", pid, binary, logPath)
				return nil
			}
		}
	}
}

func showLogs(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".dev", "run", "latest-log"))
	if err != nil {
		return err
	}
	path := string(data)
	if filepath.Dir(path) != filepath.Join(root, ".dev", "logs") {
		return errors.New("invalid log path")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 16384 {
		if _, err = f.Seek(-16384, 2); err != nil {
			return err
		}
	}
	data = make([]byte, 16384)
	n, err := f.Read(data)
	if n > 0 {
		fmt.Print(string(data[:n]))
		return nil
	}
	return err
}
