// Package history stores playback occurrences independently of autoplay history.
package history

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"asterinmusic/internal/history/historydb"
	"asterinmusic/internal/music"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const PageSize = 10

var ErrExpired = errors.New("history snapshot expired")
var ErrPage = errors.New("history page out of range")

type Record struct {
	ID                                               int64
	OccurrenceID, GuildID, VoiceID, SessionID, BotID string
	StartedAt                                        time.Time
	Entry                                            music.Entry
	Automatic                                        bool
}

type Snapshot struct {
	GuildID, VoiceID string
	Anchor, Count    int64
}

type Store struct {
	db    *sql.DB
	limit int64
}

func Open(path string, limit int) (*Store, error) {
	if limit < 1 || limit > 1000000 {
		return nil, errors.New("history retention must be between 1 and 1000000")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if len(u.Path) > 1 && u.Path[1] == ':' {
		u.Path = "/" + u.Path
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// Writes are low frequency. A bounded pool also bounds per-connection memory.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, limit: int64(limit)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("history schema is newer than this bot")
	}
	if version == 0 {
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Append(ctx context.Context, r Record) error {
	if r.OccurrenceID == "" || r.GuildID == "" || r.VoiceID == "" || r.StartedAt.IsZero() {
		return errors.New("incomplete history occurrence")
	}
	data, err := json.Marshal(r.Entry)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := historydb.New(tx)
	automatic := int64(0)
	if r.Automatic {
		automatic = 1
	}
	err = queries.InsertHistory(ctx, historydb.InsertHistoryParams{
		OccurrenceID: r.OccurrenceID, GuildID: r.GuildID, VoiceID: r.VoiceID,
		SessionID: r.SessionID, BotID: r.BotID, StartedAt: r.StartedAt.UnixMilli(), EntryJson: data, Automatic: automatic,
	})
	if err != nil {
		return err
	}
	if err = queries.PruneHistory(ctx, historydb.PruneHistoryParams{GuildID: r.GuildID, VoiceID: r.VoiceID, Offset: s.limit}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Snapshot(ctx context.Context, guild, voice string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	q := historydb.New(tx)
	anchor, err := q.HistoryAnchor(ctx, historydb.HistoryAnchorParams{GuildID: guild, VoiceID: voice})
	if err != nil {
		return Snapshot{}, err
	}
	count, err := q.CountHistory(ctx, historydb.CountHistoryParams{GuildID: guild, VoiceID: voice, ID: anchor})
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{GuildID: guild, VoiceID: voice, Anchor: anchor, Count: count}, tx.Commit()
}

func (s *Store) Page(ctx context.Context, snap Snapshot, page int) ([]Record, error) {
	if page < 1 || int64(page) > max(int64(1), (snap.Count+PageSize-1)/PageSize) {
		return nil, ErrPage
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := historydb.New(tx)
	count, err := q.CountHistory(ctx, historydb.CountHistoryParams{GuildID: snap.GuildID, VoiceID: snap.VoiceID, ID: snap.Anchor})
	if err != nil {
		return nil, err
	}
	if count != snap.Count {
		return nil, ErrExpired
	}
	rows, err := q.ListHistory(ctx, historydb.ListHistoryParams{GuildID: snap.GuildID, VoiceID: snap.VoiceID, ID: snap.Anchor, Limit: PageSize, Offset: int64(page-1) * PageSize})
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(rows))
	for _, row := range rows {
		r, err := decode(row)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, tx.Commit()
}

func (s *Store) Get(ctx context.Context, guild, voice string, id int64) (Record, error) {
	r, err := historydb.New(s.db).GetHistory(ctx, historydb.GetHistoryParams{GuildID: guild, VoiceID: voice, ID: id})
	if err != nil {
		return Record{}, err
	}
	return decode(r)
}

func decode(r historydb.PlaybackHistory) (Record, error) {
	var entry music.Entry
	if err := json.Unmarshal(r.EntryJson, &entry); err != nil {
		return Record{}, fmt.Errorf("invalid history metadata: %w", err)
	}
	return Record{ID: r.ID, OccurrenceID: r.OccurrenceID, GuildID: r.GuildID, VoiceID: r.VoiceID, SessionID: r.SessionID, BotID: r.BotID,
		StartedAt: time.UnixMilli(r.StartedAt), Entry: entry, Automatic: r.Automatic != 0}, nil
}
