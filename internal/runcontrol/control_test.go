package runcontrol

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestFailedCheckpointRejectsStop(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, c, err := Start(ctx, root, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	Ready(ctx)
	BeforeStop(ctx, func() error { return errors.New("disk failure") })
	if _, err := Request(root, "stop"); err == nil {
		t.Fatal("stop accepted")
	}
	if ctx.Err() != nil {
		t.Fatal("cancelled on checkpoint failure")
	}
	state, err := Request(root, "status")
	if err != nil || !state.Ready {
		t.Fatal(state.Ready, err)
	}
	BeforeStop(ctx, func() error {
		if ctx.Err() != nil {
			t.Error("checkpoint ran after cancellation")
		}
		return nil
	})
	if _, err := Request(root, "stop"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("not cancelled")
	}
}

func TestPlaybackStatusIsAuthenticatedAndOptional(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, c, err := Start(ctx, root, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := Request(root, "status")
	if err != nil || s.Playback != nil {
		t.Fatal("unexpected playback capability")
	}
	ObservePlayback(ctx, func() Playback { return Playback{Active: true, Started: true, Sequence: 9} })
	s, err = Request(root, "status")
	if err != nil || s.Playback == nil || s.Playback.Sequence != 9 || s.Token != "" {
		t.Fatal("invalid status")
	}
	response, err := http.Get("http://" + Address(root, false) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("playback state exposed without authentication")
	}
}

func TestSingleInstanceReadyAndGracefulStop(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, control, err := Start(ctx, root, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, other, err := Start(ctx, root, cancel); err == nil {
		other.Close()
		t.Fatal("duplicate instance accepted")
	}
	state, err := Request(root, "status")
	if err != nil || state.Ready {
		t.Fatalf("initial status: %v %v", state.Ready, err)
	}
	Ready(ctx)
	state, err = Request(root, "status")
	if err != nil || !state.Ready {
		t.Fatalf("ready: %v %v", state.Ready, err)
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Post("http://"+Address(root, false)+"/stop", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 || ctx.Err() != nil {
		t.Fatal("unauthorized stop accepted")
	}
	if _, err = Request(root, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel")
	}
	control.Close()
	if _, err = Request(root, "status"); err == nil {
		t.Fatal("closed endpoint still reported live")
	}
	_, next, err := Start(context.Background(), root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
