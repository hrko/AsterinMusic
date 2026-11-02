//go:build dave && !davego

package discordbot

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
)

const daveEnabled = true
const daveBackend = "libdave"

func daveOptions(_ *App) []bot.ConfigOpt {
	return []bot.ConfigOpt{bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(golibdave.NewSession))}
}
