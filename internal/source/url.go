package source

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type Input struct {
	URL      string `json:"url"`
	Site     string `json:"site"`
	Playlist bool   `json:"playlist"`
	// AttachedPlaylistURL is an optional follow-up action, never auto-imported.
	AttachedPlaylistURL string `json:"attached_playlist_url,omitempty"`
	AttachedMixURL      string `json:"attached_mix_url,omitempty"`
	PlaylistNotice      string `json:"playlist_notice,omitempty"`
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var nicoID = regexp.MustCompile(`^(?:sm|nm|so)[0-9]+$`)
var playlistID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Parse accepts only explicit supported page URL forms, never arbitrary URLs,
// yt-dlp pseudo-URLs, credentials, nonstandard ports, or local files.
func Parse(raw string) (Input, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return Input{}, errors.New("expected a supported public video or playlist URL")
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be":
		site := "youtube"
		canonical := "www.youtube.com"
		if host == "music.youtube.com" {
			site = "youtube_music"
			canonical = host
		}
		id := ""
		if host == "youtu.be" {
			id = strings.TrimPrefix(u.Path, "/")
		} else if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else if strings.HasPrefix(u.Path, "/shorts/") {
			id = strings.TrimPrefix(u.Path, "/shorts/")
		}
		if videoID.MatchString(id) {
			input := Input{URL: "https://" + canonical + "/watch?v=" + id, Site: site}
			if list := u.Query().Get("list"); supportedPlaylist(list) {
				input.AttachedPlaylistURL = "https://" + canonical + "/playlist?list=" + list
			} else if strings.HasPrefix(list, "RD") && playlistID.MatchString(list) {
				input.AttachedMixURL = input.URL + "&list=" + list
			} else if list != "" {
				input.PlaylistNotice = "このURLのリストは取り込み対象外です。指定した1曲だけを追加します。"
			}
			return input, nil
		}
		if u.Path == "/playlist" {
			list := u.Query().Get("list")
			if supportedPlaylist(list) {
				return Input{URL: "https://" + canonical + "/playlist?list=" + list, Site: site, Playlist: true}, nil
			}
		}
	case "www.nicovideo.jp", "nicovideo.jp", "nico.ms":
		id := strings.TrimPrefix(u.Path, "/watch/")
		if host == "nico.ms" {
			id = strings.TrimPrefix(u.Path, "/")
		}
		if nicoID.MatchString(id) {
			return Input{URL: "https://www.nicovideo.jp/watch/" + id, Site: "niconico"}, nil
		}
	}
	return Input{}, errors.New("unsupported URL (use a single video or a public YouTube playlist)")
}

func supportedPlaylist(id string) bool {
	return playlistID.MatchString(id) && !strings.HasPrefix(id, "RD") && id != "WL" && id != "LL"
}
