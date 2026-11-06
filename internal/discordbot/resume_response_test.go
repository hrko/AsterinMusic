package discordbot

import (
	"asterinmusic/internal/audio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func TestResumeCommandResponseFailuresDoNotOwnPlayback(t *testing.T) {
	s := resumeSession(t)
	a := s.app
	a.sessions = map[snowflake.ID]*session{2: s}
	updates := make(chan discord.MessageUpdate, 4)
	a.client.Rest = rest.New(rest.NewClient("fixture", rest.WithHTTPClient(&http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
		var update discord.MessageUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			t.Error(err)
		}
		updates <- update
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":50013,"message":"fixture"}`)), Request: r}, nil
	})})))
	entered, send, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblockSend := sync.OnceFunc(func() { close(send) })
	unblockPlayback := sync.OnceFunc(func() { close(release) })
	defer unblockSend()
	defer unblockPlayback()
	s.playAudio = func(ctx context.Context, _ voice.Conn, _ audio.Producer) error {
		close(entered)
		<-send
		if err := s.sendPacket(ctx, func() error { return nil }); err != nil {
			return err
		}
		<-release
		return nil
	}
	var interaction discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(`{"id":"10","application_id":"8","guild_id":"2","type":2,"token":"fixture","channel":{"id":"4","type":0},"user":{"id":"1","username":"fixture"},"data":{"id":"5","name":"play","type":1}}`), &interaction); err != nil {
		t.Fatal(err)
	}
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		a.onCommand(&events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: interaction, Respond: func(kind discord.InteractionResponseType, _ discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			if kind != discord.InteractionResponseTypeDeferredCreateMessage {
				t.Error("resume command was not deferred")
			}
			return nil
		}})
	}()
	readUpdate := func(want string) {
		t.Helper()
		select {
		case update := <-updates:
			if update.Content == nil || *update.Content != want {
				t.Fatalf("response=%v want=%q", update.Content, want)
			}
		case <-time.After(time.Second):
			t.Fatal("response update was not attempted")
		}
	}
	readUpdate((resumeResult{state: resumeAccepted}).message())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("acceptance update failure prevented playback preparation")
	}
	select {
	case <-updates:
		t.Fatal("success response before first audio write")
	default:
	}
	unblockSend()
	readUpdate((resumeResult{state: resumeStarted}).message())
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("failed result update retained the interaction handler")
	}
	s.mu.Lock()
	playing, done := s.running && s.started && !s.stopped, s.done
	s.mu.Unlock()
	if !playing {
		t.Fatal("result update failure stopped playback")
	}
	unblockPlayback()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback did not finish after fixture release")
	}
}
