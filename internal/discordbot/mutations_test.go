package discordbot

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCheckpointRejectsUserMutations(t *testing.T) {
	s := newSession(&App{ctx: context.Background()}, 1)
	if err := s.setVolume(30); err != nil {
		t.Fatal(err)
	}
	if err := s.checkpoint(filepath.Join(t.TempDir(), "state.json")); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"volume": func() error { return s.setVolume(80) },
		"stop":   s.stop,
		"skip":   s.skip,
	} {
		if err := mutate(); err == nil || err.Error() != "再起動処理中です。" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if s.volume.Percent() != 30 {
		t.Fatal("volume changed after checkpoint")
	}
}
