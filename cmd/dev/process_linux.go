package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func runningBots(root string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		exe, e := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if e != nil {
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			name, ne := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
			if ne != nil {
				if errors.Is(ne, os.ErrNotExist) {
					continue
				}
				return nil, ne
			}
			if strings.HasPrefix(string(name), "rainmusic-voice") {
				return nil, fmt.Errorf("cannot inspect Bot PID %d: %w", pid, e)
			}
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if filepath.Dir(exe) == filepath.Join(root, "bin") && strings.HasPrefix(filepath.Base(exe), "rainmusic-voice") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
func detached(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
