package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"asterinmusic/internal/music"
)

func record(n int) Record {
	return Record{OccurrenceID: fmt.Sprint(n), GuildID: "1", VoiceID: "2", BotID: "3", SessionID: "session",
		StartedAt: time.Unix(int64(n), 0), Entry: music.Entry{ID: fmt.Sprint(n), Track: music.Track{Title: fmt.Sprintf("曲 %d", n), URL: "https://www.youtube.com/watch?v=abcdefghijk"}}}
}

func TestStoreRetentionScopeSnapshotAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "履歴 #1.db")
	s, err := Open(path, 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for n := 1; n <= 12; n++ {
		if err := s.Append(ctx, record(n)); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Snapshot(ctx, "1", "2")
	if err != nil || snap.Count != 12 {
		t.Fatal(snap, err)
	}
	page, err := s.Page(ctx, snap, 2)
	if err != nil || len(page) != 2 || page[0].OccurrenceID != "2" {
		t.Fatal(page, err)
	}
	r := record(12)
	r.Entry.Track.Title = "changed"
	r.StartedAt = time.Now()
	if err := s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	page, err = s.Page(ctx, snap, 1)
	if err != nil || page[0].Entry.Track.Title != "曲 12" {
		t.Fatal(page, err)
	}
	for _, scope := range [][2]string{{"9", "2"}, {"1", "9"}} {
		if _, err = s.Get(ctx, scope[0], scope[1], page[0].ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("cross-scope lookup", err)
		}
	}
	r = record(13)
	r.VoiceID = "other"
	if err = s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(ctx, snap, 1); err != nil {
		t.Fatal("other VC invalidated snapshot", err)
	}
	r = record(14)
	if err = s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(ctx, snap, 1); !errors.Is(err, ErrExpired) {
		t.Fatal("pruning silently shifted page", err)
	}
	s.Close()
	s, err = Open(path, 12)
	if err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx, "1", "2")
	if err != nil || snap.Count != 12 {
		t.Fatal(snap, err)
	}
	other, err := s.Snapshot(ctx, "1", "other")
	if err != nil || other.Count != 1 {
		t.Fatal(other, err)
	}
	if _, err = s.Page(ctx, snap, 0); !errors.Is(err, ErrPage) {
		t.Fatal(err)
	}
	if _, err = s.Page(ctx, snap, 3); !errors.Is(err, ErrPage) {
		t.Fatal(err)
	}
}

func TestTenThousandRowsAndStablePages(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"), 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// Seed a large real database in one transaction; exercise retention via Append.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `WITH RECURSIVE nums(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM nums WHERE n<9999)
	INSERT INTO playback_history(occurrence_id,guild_id,voice_id,session_id,bot_id,started_at,entry_json,automatic)
	SELECT CAST(n AS TEXT),'1','2','s','b',n,CAST('{}' AS BLOB),0 FROM nums`)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, "1", "2")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Append(ctx, record(10000)); err != nil {
		t.Fatal(err)
	}
	page, err := s.Page(ctx, snap, 1000)
	if err != nil || len(page) != 9 || page[8].OccurrenceID != "1" {
		t.Fatal(page, err)
	}
	snap, err = s.Snapshot(ctx, "1", "2")
	if err != nil || snap.Count != 10000 {
		t.Fatal(snap, err)
	}
	page, err = s.Page(ctx, snap, 1000)
	if err != nil || len(page) != 10 {
		t.Fatal(len(page), err)
	}
	if err = s.Append(ctx, record(10001)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(ctx, snap, 1000); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestMigrationRefusesFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if future, err := Open(path, 100); err == nil {
		future.Close()
		t.Fatal("future schema accepted")
	}
}
