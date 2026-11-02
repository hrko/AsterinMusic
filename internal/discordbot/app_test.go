package discordbot

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"testing"
	"time"
)

func TestListenersDoNotBlockGatewayDispatcher(t *testing.T) {
	manager := bot.NewEventManager(&bot.Client{}, eventOptions()...)
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	manager.AddEventListeners(bot.NewListenerFunc(func(*events.Ready) { close(entered); <-release }))
	go func() { manager.DispatchEvent(&events.Ready{}); close(returned) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("listener not called")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Gateway dispatcher blocked on command listener")
	}
}
