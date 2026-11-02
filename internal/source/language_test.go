package source

import (
	"context"
	"io"
	"testing"
)

func TestMetadataLanguage(t *testing.T) {
	for _, tc := range []struct{ language, url, want string }{
		{"ja", "https://www.youtube.com/watch?v=abcdefghijk", "youtube:lang=ja"},
		{"en", "https://music.youtube.com/watch?v=abcdefghijk", "youtube:lang=en"},
		{"ja", "https://www.youtube.com/playlist?list=PLabcdefghijk", "youtube:lang=ja"},
		{"auto", "https://www.youtube.com/watch?v=abcdefghijk", ""},
		{"provider_default", "https://www.youtube.com/watch?v=abcdefghijk", ""},
		{"ja", "https://www.nicovideo.jp/watch/sm9", ""},
	} {
		t.Run(tc.language+tc.url, func(t *testing.T) {
			c := New("fake")
			c.MetadataLanguage = tc.language
			c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
				if got := argValue(args, "--extractor-args"); got != tc.want {
					t.Fatalf("got %q want %q", got, tc.want)
				}
				_, err := io.WriteString(out, `{"id":"abcdefghijk","title":"test"}`)
				return err
			}
			if _, err := c.Inspect(context.Background(), tc.url); err != nil {
				t.Fatal(err)
			}
		})
	}
	c := New("fake")
	c.Run = func(_ context.Context, _ string, args []string, _ io.Reader, out io.Writer) error {
		if argValue(args, "--extractor-args") != "youtube:lang=ja" {
			t.Fatal("Mix missing language")
		}
		_, err := io.WriteString(out, `{"id":"abcdefghijk","title":"test"}`)
		return err
	}
	if _, err := c.InspectMix(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk"); err != nil {
		t.Fatal(err)
	}
}
