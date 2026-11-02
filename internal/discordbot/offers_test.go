package discordbot

import (
	"rainmusic/internal/music"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOfferSingleClaimAndRetry(t *testing.T) {
	o := newOffers()
	v := o.add(1, 2, "list", "track")
	o.bind(v.ID, 3)
	if _, err := o.claim(v.ID, 1, 4, 3, time.Now()); err == nil {
		t.Fatal("other requester accepted")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := o.claim(v.ID, 1, 2, 3, time.Now()); err == nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	o.finish(v.ID, false)
	if _, err := o.claim(v.ID, 1, 2, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	o.finish(v.ID, true)
	if _, err := o.claim(v.ID, 1, 2, 3, time.Now()); err == nil {
		t.Fatal("reused")
	}
	v = o.add(1, 2, "list", "track")
	o.bind(v.ID, 3)
	if _, err := o.claim(v.ID, 1, 2, 3, time.Now().Add(16*time.Minute)); err == nil {
		t.Fatal("expired")
	}
}
func TestExcludeOnlyFirst(t *testing.T) {
	r, n := excludeFirst([]music.Track{{URL: "a"}, {URL: "b"}, {URL: "a"}}, "a")
	if n != 1 || len(r) != 2 || r[0].URL != "b" || r[1].URL != "a" {
		t.Fatal(r, n)
	}
}
