package main

import (
	"asterinmusic/internal/runcontrol"
	"testing"
)

func TestRestartWaitsForAudibleNextTrack(t *testing.T) {
	before := runcontrol.Playback{Active: true, Started: true, Sequence: 7}
	for _, after := range []runcontrol.Playback{{Active: true, Started: true, Sequence: 7}, {Active: true, Started: false, Sequence: 7}, {Active: true, Started: false, Sequence: 8}} {
		if boundaryReached(before, after) {
			t.Fatal("restarted before next audio packet")
		}
	}
	if !boundaryReached(before, runcontrol.Playback{Active: true, Started: true, Sequence: 8}) {
		t.Fatal("did not restart at next track")
	}
	if !boundaryReached(before, runcontrol.Playback{Active: false, Sequence: 7}) {
		t.Fatal("waited forever after playback stopped")
	}
}
