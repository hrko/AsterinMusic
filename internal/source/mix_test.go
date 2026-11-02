package source

import (
	"asterinmusic/internal/music"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMixInput(t *testing.T) {
	for _, raw := range []string{"https://youtu.be/abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk&list=PL123", "https://music.youtube.com/watch?v=abcdefghijk"} {
		i, err := MixInput(raw)
		if err != nil || !strings.HasSuffix(i.URL, "&list=RDabcdefghijk") || !i.Playlist {
			t.Fatal(i, err)
		}
	}
	raw := "https://music.youtube.com/watch?v=abcdefghijk&list=RDAMVMsomething&index=8"
	i, err := MixInput(raw)
	if err != nil || i.URL != "https://music.youtube.com/watch?v=abcdefghijk&list=RDAMVMsomething" {
		t.Fatal(i, err)
	}
	for _, raw := range []string{"https://youtube.com/playlist?list=PL123", "https://nico.ms/sm9", "file:///tmp/a", "https://youtube.com.evil/watch?v=abcdefghijk"} {
		if _, err := MixInput(raw); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestMixInspectionAndSingleIsolation(t *testing.T) {
	c := New("fake")
	calls := 0
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		calls++
		joined := strings.Join(args, " ")
		if calls == 1 {
			if !strings.Contains(joined, "--no-playlist") || strings.Contains(joined, "list=") {
				t.Fatal(args)
			}
		} else {
			if !strings.Contains(joined, "--yes-playlist") || !strings.Contains(joined, "--playlist-end 100") || !strings.HasSuffix(joined, "watch?v=abcdefghijk&list=RDabcdefghijk") {
				t.Fatal(args)
			}
		}
		io.WriteString(out, "{\"id\":\"abcdefghijk\",\"title\":\"seed\"}\n")
		if calls == 2 {
			io.WriteString(out, "{\"id\":\"bcdefghijkl\",\"title\":\"next\"}\n{\"id\":\"cdefghijklm\",\"title\":\"live\",\"is_live\":true}\n")
			return errors.New("partial")
		}
		return nil
	}
	raw := "https://youtube.com/watch?v=abcdefghijk&list=RDabcdefghijk"
	single, err := c.Inspect(context.Background(), raw)
	if err != nil || len(single.Tracks) != 1 || single.Input.AttachedMixURL == "" {
		t.Fatal(single, err)
	}
	mix, err := c.InspectMix(context.Background(), raw)
	if err == nil || !mix.Partial || mix.Skipped != 1 || len(mix.Tracks) != 2 || strings.Contains(mix.Tracks[1].URL, "list=") {
		t.Fatal(mix, err)
	}
}
func TestMixSelection(t *testing.T) {
	track := func(host, id string) music.Track { return music.Track{URL: "https://" + host + "/watch?v=" + id} }
	seed := track("www.youtube.com", "abcdefghijk")
	b := track("www.youtube.com", "bcdefghijkl")
	c := track("www.youtube.com", "cdefghijklm")
	input := []music.Track{seed, b, track("music.youtube.com", "bcdefghijkl"), seed, c}
	r := SelectMix(input, seed, 2, true)
	if len(r.Tracks) != 2 || r.Tracks[0] != seed || r.Tracks[1] != b || r.SeedExcluded != 2 || r.Duplicates != 1 || r.OverLimit != 1 {
		t.Fatal(r)
	}
	r = SelectMix(input, seed, 20, false)
	if len(r.Tracks) != 2 || r.Tracks[0] != b || r.Tracks[1] != c {
		t.Fatal(r)
	}
	r = SelectMix(input, seed, 1, true)
	if len(r.Tracks) != 1 || r.Tracks[0] != seed {
		t.Fatal(r)
	}
	for _, count := range []int{0, 51} {
		if len(SelectMix(input, seed, count, true).Tracks) != 0 {
			t.Fatal(count)
		}
	}
}
