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
}
