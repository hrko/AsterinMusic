package source

import (
	"asterinmusic/internal/logging"
	"asterinmusic/internal/music"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetadataFailureLogsSafeOutcome(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			logger, err := logging.New(&out, logging.Options{Format: format, Level: "debug"})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, "RESPONSE_SECRET") }))
			defer server.Close()
			d := NewYouTubeData("API_KEY_SECRET")
			d.endpoint = server.URL
			d.Logger = logging.Component(logger, "source")
			tracks := []music.Track{{URL: "https://www.youtube.com/watch?v=abcdefghijk", Site: "youtube", Title: "PRIVATE_TITLE"}}
			d.Enrich(t.Context(), tracks, "ja")
			for _, value := range []string{"API_KEY_SECRET", "RESPONSE_SECRET", "PRIVATE_TITLE", "abcdefghijk", server.URL} {
				if strings.Contains(out.String(), value) {
					t.Fatalf("unsafe output: %s", out.String())
				}
			}
			for _, value := range []string{"source.metadata.fallback", "use_extractor_metadata", "403"} {
				if !strings.Contains(out.String(), value) {
					t.Fatalf("missing result: %s", out.String())
				}
			}
			if tracks[0].Title != "PRIVATE_TITLE" {
				t.Fatal("fallback metadata changed")
			}
		})
	}
}

func TestMetadataFailureSeriesIsSummarizedOnRecovery(t *testing.T) {
	var out bytes.Buffer
	logger, _ := logging.New(&out, logging.Options{Format: "json"})
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count < 3 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	}))
	defer server.Close()
	d := NewYouTubeData("PRIVATE")
	d.endpoint = server.URL
	d.Logger = logging.Component(logger, "source")
	for range 3 {
		d.blockedUntil = time.Time{} // Exercise logging independently of request backoff.
		d.Enrich(t.Context(), []music.Track{{URL: "https://www.youtube.com/watch?v=abcdefghijk", Site: "youtube"}}, "ja")
	}
	text := out.String()
	if strings.Count(text, `"event":"source.metadata.fallback"`) != 1 || !strings.Contains(text, `"suppressed":1`) || !strings.Contains(text, `"event":"logging.repetition.recovered"`) {
		t.Fatalf("missing aggregate or recovery: %s", text)
	}
}
