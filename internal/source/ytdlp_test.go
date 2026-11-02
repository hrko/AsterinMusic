package source

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestURLs(t *testing.T) {
	for _, raw := range []string{"file:///tmp/a", "https://youtube.com.evil.test/watch?v=abcdefghijk", "https://user@youtube.com/watch?v=abcdefghijk", "https://youtube.com:443/watch?v=abcdefghijk", "ytsearch:music", "https://youtube.com/playlist?list=RD123", "https://www.nicovideo.jp/mylist/123"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	i, err := Parse("https://music.youtube.com/watch?v=abcdefghijk&list=PL123")
	if err != nil || i.Playlist || strings.Contains(i.URL, "list=") {
		t.Fatalf("%v %v", i, err)
	}
	if _, err := Parse("https://nico.ms/sm123"); err != nil {
		t.Fatal(err)
	}
}

func TestAttachedPlaylist(t *testing.T) {
	for _, hostPath := range []string{"www.youtube.com/watch?v=abcdefghijk", "youtu.be/abcdefghijk?x=1", "www.youtube.com/shorts/abcdefghijk?x=1", "music.youtube.com/watch?v=abcdefghijk"} {
		for _, list := range []string{"PL123", "RD123", "WL", "LL", "bad!", ""} {
			input, err := Parse("https://" + hostPath + "&list=" + list + "&index=3")
			if err != nil {
				t.Fatal(err)
			}
			if input.Playlist || strings.Contains(input.URL, "list=") {
				t.Fatalf("not single-track: %+v", input)
			}
			if list == "PL123" {
				attached, err := Parse(input.AttachedPlaylistURL)
				if err != nil || !attached.Playlist || attached.Site != input.Site {
					t.Fatalf("lost attachment: %+v %v", input, err)
				}
			} else if input.AttachedPlaylistURL != "" {
				t.Fatalf("unsupported attachment: %+v", input)
			}
		}
	}
}

func TestInspectDoesNotImportAttachedPlaylist(t *testing.T) {
	c := New("yt-dlp")
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--no-playlist") || strings.Contains(joined, "list=") || strings.Contains(joined, "--flat-playlist") {
			t.Fatal(args)
		}
		_, err := io.WriteString(out, `{"id":"abcdefghijk","title":"one"}`)
		return err
	}
	r, err := c.Inspect(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk&list=PL123")
	if err != nil || len(r.Tracks) != 1 || r.Input.AttachedPlaylistURL != "https://www.youtube.com/playlist?list=PL123" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestPlaylistPartialResult(t *testing.T) {
	c := New("yt-dlp")
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		if !reflect.DeepEqual(args[len(args)-2:], []string{"--", "https://www.youtube.com/playlist?list=PL123"}) {
			t.Fatal(args)
		}
		io.WriteString(out, "{\"id\":\"abcdefghijk\",\"title\":\"first\"}\n{\"id\":\"bcdefghijkl\",\"title\":\"private\",\"availability\":\"private\"}\n")
		return errors.New("one entry failed")
	}
	r, err := c.Inspect(context.Background(), "https://youtube.com/playlist?list=PL123")
	if err == nil || !r.Partial || r.Skipped != 1 || len(r.Tracks) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRejectLiveAndMalformedOutput(t *testing.T) {
	for _, data := range []string{`{"id":"abcdefghijk","title":"live","is_live":true}`, `garbage`} {
		c := New("yt-dlp")
		c.Run = func(_ context.Context, _ string, _ []string, _ io.Reader, w io.Writer) error {
			_, err := io.WriteString(w, data)
			return err
		}
		if _, err := c.Inspect(context.Background(), "https://youtu.be/abcdefghijk"); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestStreamRejectsEmptySuccess(t *testing.T) {
	c := New("fake")
	c.Run = func(context.Context, string, []string, io.Reader, io.Writer) error { return nil }
	if err := c.Stream(context.Background(), "https://www.nicovideo.jp/watch/sm9", io.Discard); err == nil {
		t.Fatal("empty download accepted")
	}
}

func TestMixAttachmentWithoutImport(t *testing.T) {
	i, err := Parse("https://www.youtube.com/watch?v=abcdefghijk&list=RDabcdefghijk")
	if err != nil || i.AttachedMixURL == "" || i.AttachedPlaylistURL != "" || i.Playlist {
		t.Fatal(i, err)
	}
}
