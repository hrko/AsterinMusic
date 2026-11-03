package discordbot

import (
	"asterinmusic/internal/music"
	"strings"
	"testing"
)

func TestLinkLabelBrackets(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"曲 [Day2]", "曲 [Day2]"},
		{"[外[内]]", "[外[内]]"},
		{"曲 ]", "曲 ]"},
		{"曲 [", "曲 ［"},
		{"[外[内]", "［外[内]"},
		{"[a] [b", "[a] ［b"},
		{"*曲* @everyone", "\\*曲\\* ＠everyone"},
	} {
		if got := escapeLinkLabel(tc.in); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
	track := music.Track{Title: "曲 [" + strings.Repeat("長", 100) + "]", URL: "https://www.youtube.com/watch?v=abcdefghijk"}
	if got := trackLink(track, 10); !strings.Contains(got, "曲 ［") {
		t.Fatal(got)
	}
	compact := compactQueueTrack(track)
	if got := escapeLinkLabel(compact.Title); !strings.Contains(got, "曲 ［") {
		t.Fatal(got)
	}
}
