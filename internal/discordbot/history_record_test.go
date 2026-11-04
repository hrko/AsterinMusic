package discordbot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"asterinmusic/internal/history"
	"asterinmusic/internal/music"
)

func TestHistoryFirstPacketAndManagedResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := history.Open(filepath.Join(root, "history.sqlite"), 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	w := history.NewWriter(store.Append, nil)
	defer w.Shutdown(ctx)
	a := &App{ctx: ctx, historyWriter: w}
	s := newSession(a, 1)
	s.channel = 2
	s.running = true
	entry := music.Entry{ID: "first", Track: autoTrack("abcdefghijk"), AddedAt: time.Now()}
	s.current = &entry
	if err = s.sendPacket(ctx, func() error { return errors.New("failed write") }); err == nil {
		t.Fatal("send failure lost")
	}
	w.Flush(ctx)
	snap, _ := store.Snapshot(ctx, "1", "2")
	if snap.Count != 0 {
		t.Fatal("failed packet was recorded")
	}
	if err = s.sendPacket(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.sendPacket(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Snapshot(ctx, "1", "2")
	first, err := store.Page(ctx, snap, 1)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	path := filepath.Join(root, "checkpoint.json")
	if err = s.checkpoint(path); err != nil {
		t.Fatal(err)
	}
	v, err := readPlaybackCheckpoint(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := newSession(a, 1)
	r.channel = 2
	r.running = true
	r.restoreSettings(v)
	r.current = &entry
	if err = r.sendPacket(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Snapshot(ctx, "1", "2")
	rows, err := store.Page(ctx, snap, 1)
	if err != nil || len(rows) != 1 || !rows[0].StartedAt.Equal(first[0].StartedAt) {
		t.Fatal(rows, err)
	}
	entry.ID = "new-addition"
	entry.AddedAt = entry.AddedAt.Add(time.Second)
	r.started = false
	if err = r.sendPacket(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Snapshot(ctx, "1", "2")
	if snap.Count != 2 {
		t.Fatal("manual re-addition suppressed", snap)
	}
}
