package source

import (
	"asterinmusic/internal/music"
	"context"
	"errors"
	"net/url"
)

const DefaultMixCount = 20
const MaxMixCount = 50

// MixInput preserves an attached Mix; synthesis is exclusive to /mix.
func MixInput(raw string) (Input, error) {
	i, err := Parse(raw)
	if err != nil {
		return Input{}, err
	}
	if i.Playlist || (i.Site != "youtube" && i.Site != "youtube_music") {
		return Input{}, errors.New("MixにはYouTube / YouTube Musicの動画URLを指定してください。")
	}
	if i.AttachedMixURL != "" {
		i.URL = i.AttachedMixURL
	} else {
		u, _ := url.Parse(i.URL)
		i.URL += "&list=RD" + u.Query().Get("v")
	}
	i.Playlist = true
	return i, nil
}
func (c *Client) InspectMix(ctx context.Context, raw string) (Result, error) {
	i, err := MixInput(raw)
	if err != nil {
		return Result{}, err
	}
	return c.inspect(ctx, i)
}

type MixSelection struct {
	Tracks                              []music.Track
	Duplicates, SeedExcluded, OverLimit int
}

// SelectMix keeps the first occurrence of each ID across YouTube hostnames.
// seed is included first for /mix, and excluded everywhere for a button.
func SelectMix(candidates []music.Track, seed music.Track, count int, includeSeed bool) MixSelection {
	var r MixSelection
	if count < 1 || count > MaxMixCount {
		return r
	}
	key := func(t music.Track) string {
		u, e := url.Parse(t.URL)
		if e == nil && u.Query().Get("v") != "" {
			return u.Query().Get("v")
		}
		return t.URL
	}
	seedID := key(seed)
	seen := map[string]bool{seedID: true}
	if includeSeed {
		r.Tracks = append(r.Tracks, seed)
	}
	for _, t := range candidates {
		id := key(t)
		if id == seedID {
			r.SeedExcluded++
			continue
		}
		if seen[id] {
			r.Duplicates++
			continue
		}
		seen[id] = true
		if len(r.Tracks) < count {
			r.Tracks = append(r.Tracks, t)
		} else {
			r.OverLimit++
		}
	}
	return r
}
