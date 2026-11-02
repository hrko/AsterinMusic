package discordbot

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"rainmusic/internal/music"
)

type offer struct {
	ID, URL, TrackURL    string
	Guild, User, Message snowflake.ID
	Expires              time.Time
	Busy, Used           bool
	Insertion            music.Insertion
}
type offers struct {
	mu    sync.Mutex
	items map[string]*offer
}

func newOffers() *offers { return &offers{items: make(map[string]*offer)} }
func (o *offers) add(guild, user snowflake.ID, url, track string, position ...music.Insertion) offer {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic(err)
	}
	v := offer{ID: hex.EncodeToString(b[:]), URL: url, TrackURL: track, Guild: guild, User: user, Expires: time.Now().Add(15 * time.Minute)}
	if len(position) > 0 {
		v.Insertion = position[0]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.items[v.ID] = &v
	return v
}
func (o *offers) bind(id string, message snowflake.ID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v := o.items[id]; v != nil {
		v.Message = message
	}
}
func (o *offers) claim(id string, guild, user, message snowflake.ID, now time.Time) (offer, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := o.items[id]
	if v == nil || !now.Before(v.Expires) {
		return offer{}, errors.New("ボタンの期限が切れました。URLを指定して再実行してください。")
	}
	if v.Guild != guild || v.User != user || v.Message != message {
		return offer{}, errors.New("このボタンは曲を追加した本人だけが操作できます。")
	}
	if v.Busy || v.Used {
		return offer{}, errors.New("取り込み中、または取り込み済みです。")
	}
	v.Busy = true
	return *v, nil
}
func (o *offers) finish(id string, used bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v := o.items[id]; v != nil {
		v.Busy = false
		v.Used = used
	}
}
func (o *offers) prune(now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, v := range o.items {
		if !v.Busy && !now.Before(v.Expires) {
			delete(o.items, id)
		}
	}
}

func excludeFirst(tracks []music.Track, url string) ([]music.Track, int) {
	result := make([]music.Track, 0, len(tracks))
	removed := 0
	for _, t := range tracks {
		if removed == 0 && trackIdentity(t.URL) == trackIdentity(url) {
			removed++
			continue
		}
		result = append(result, t)
	}
	return result, removed
}

func trackIdentity(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		if id := u.Query().Get("v"); id != "" {
			return id
		}
	}
	return raw
}
