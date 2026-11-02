//go:build windows

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func runningBots(root string) ([]int, error) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var pids []int
	for err = windows.Process32First(h, &entry); err == nil; err = windows.Process32Next(h, &entry) {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if !strings.HasPrefix(name, "rainmusic-voice") {
			continue
		}
		process, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if e != nil {
			if errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
				continue
			}
			return nil, fmt.Errorf("cannot inspect possible Bot PID %d: %w", entry.ProcessID, e)
		}
		buffer := make([]uint16, 32768)
		size := uint32(len(buffer))
		e = windows.QueryFullProcessImageName(process, 0, &buffer[0], &size)
		windows.CloseHandle(process)
		if e != nil {
			return nil, e
		}
		if strings.EqualFold(filepath.Dir(windows.UTF16ToString(buffer[:size])), filepath.Join(root, "bin")) {
			pids = append(pids, int(entry.ProcessID))
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return pids, nil
}
func detached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
