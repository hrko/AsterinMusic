package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupProtectsActiveAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"bin", filepath.Join(".dev", "logs"), filepath.Join(".dev", "state")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 12; i++ {
		for _, name := range []string{filepath.Join("bin", fmt.Sprintf("asterinmusic-voice-%d.exe", i)), filepath.Join(".dev", "logs", fmt.Sprintf("bot-%d.log", i))} {
			if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"bin/custom.exe", "bin/asterinmusic-voice-current.exe", ".dev/state/queue.json", ".dev/logs/notes.log"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(root, "bin", "asterinmusic-voice-1.exe")
	log := filepath.Join(root, ".dev", "logs", "bot-1.log")
	plan, err := cleanupPlan(root, active, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 9 {
		t.Fatalf("got %d removals, want 8 binaries and 1 log", len(plan))
	}
	for _, file := range plan {
		if file.path == active || file.path == log || file.generation >= 10 {
			t.Fatal("protected generation selected")
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".dev", "state", "queue.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".dev", "run"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".dev", "run", "latest-log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	cleanupApply = false
	t.Cleanup(func() { cleanupApply = false })
	if err := cleanupArtifacts(root, active); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan {
		if _, err := os.Stat(file.path); err != nil {
			t.Fatal("preview removed a file", err)
		}
	}
	cleanupApply = true
	if err := cleanupArtifacts(root, active); err != nil {
		t.Fatal(err)
	}
	for _, file := range plan {
		if _, err := os.Stat(file.path); !os.IsNotExist(err) {
			t.Fatal("candidate was not removed", err)
		}
	}
	for _, name := range []string{active, log, filepath.Join(root, "bin", "asterinmusic-voice-12.exe"), filepath.Join(root, ".dev", "state", "queue.json"), filepath.Join(root, "bin", "custom.exe")} {
		if _, err := os.Stat(name); err != nil {
			t.Fatal("protected file removed", err)
		}
	}
}

func TestCleanupRejectsRedirectedDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "bin")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := cleanupPlan(root, "", ""); err == nil {
		t.Fatal("redirected artifact directory accepted")
	}
}

func TestCleanupRequiresActiveLog(t *testing.T) {
	root := t.TempDir()
	if err := cleanupArtifacts(root, filepath.Join(root, "bin", "active.exe")); err == nil {
		t.Fatal("cleanup accepted unknown active log")
	}
}
