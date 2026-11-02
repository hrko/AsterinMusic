//go:build !dave && !davego

package discordbot

import "github.com/disgoorg/disgo/bot"

const daveEnabled = false
const daveBackend = "disabled"

func daveOptions(_ *App) []bot.ConfigOpt { return nil }
