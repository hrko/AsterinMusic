package discordbot

import (
	"asterinmusic/internal/logging"
	"bytes"
	"encoding/json"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingVoiceDiagnostic struct{ entered, release chan struct{} }

func (w blockingVoiceDiagnostic) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("voice.membership.received")) {
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}

func TestMembershipDiagnosticDoesNotHoldStateLock(t *testing.T) {
	w := blockingVoiceDiagnostic{make(chan struct{}), make(chan struct{})}
	logger, _ := logging.New(w, logging.Options{Format: "json", Level: "debug"})
	c := newVoiceConnection(1, 2)
	c.phase = voiceReady
	c.channel = 3
	e := stateEvent(3)
	c.state = &e
	c.logger = logging.Component(logger, "voice")
	finished := make(chan struct{})
	go func() { c.HandleVoiceStateUpdate(e); close(finished) }()
	defer func() { close(w.release); <-finished }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("missing diagnostic")
	}
	read := make(chan struct{})
	go func() { c.ChannelID(); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("diagnostic held state lock")
	}
}

type logBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

func TestVoiceLogsFollowChannelAndExcludeCredentials(t *testing.T) {
	var out logBuffer
	logger, err := logging.New(&out, logging.Options{Format: "json", Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	c := testVoiceController(t)
	c.logger = logging.Component(logger, "voice").With("bot_id", "2", "guild_id", "1")
	for _, id := range []snowflake.ID{3, 4, 3} {
		e := stateEvent(id)
		e.SessionID = "PRIVATE_SESSION"
		c.HandleVoiceStateUpdate(e)
		c.HandleVoiceServerUpdate(serverEvent("PRIVATE_TOKEN"))
		waitVoice(t, c)
	}
	text := out.String()
	if strings.Contains(text, "PRIVATE_") {
		t.Fatal("voice credentials leaked")
	}
	var ready []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] == "voice.connection.ready" {
			ready = append(ready, event["channel_id"].(string))
			if event["guild_id"] != "1" || event["bot_id"] != "2" {
				t.Fatalf("wrong scope: %v", event)
			}
		}
	}
	if strings.Join(ready, ",") != "3,4,3" {
		t.Fatalf("stale channel attributes: %v", ready)
	}
}
