package discordbot

import (
	"asterinmusic/internal/helpui"
	"context"
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"strconv"
	"strings"
	"time"
)

const helpTTL = 15 * time.Minute

// Called before the Gateway opens; the catalog is immutable thereafter.
func (a *App) loadHelpLinks() {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	commands, err := a.client.Rest.GetGuildCommands(a.config.ApplicationID, a.config.GuildID, false, rest.WithCtx(ctx))
	if err != nil {
		a.logOperationFailure("interaction.help_links", err)
		return
	}
	links := map[string]string{}
	for _, c := range commands {
		if c.Type() == discord.ApplicationCommandTypeSlash {
			links[c.Name()] = c.ID().String()
		}
	}
	c, err := helpui.New(Commands(), links)
	if err != nil {
		a.logOperationFailure("interaction.help_catalog", err)
		return
	}
	a.helpCatalog = c
}

func (a *App) helpMessage(pageID, owner string, expires int64) discord.MessageCreate {
	if pageID == "" {
		pageID = helpui.Home
	}
	p, ok := a.helpCatalog.Find(pageID)
	notice := ""
	if !ok {
		p, _ = a.helpCatalog.Find(helpui.Home)
		notice = "指定したページはありません。下のメニューから選び直してください。"
	}
	var guides, commands []discord.StringSelectMenuOption
	for _, page := range a.helpCatalog.Pages() {
		o := discord.NewStringSelectMenuOption(page.Name, page.ID).WithDescription(page.Summary).WithDefault(page.ID == p.ID)
		if page.Guide {
			guides = append(guides, o)
		} else {
			commands = append(commands, o)
		}
	}
	id := func(kind string) string { return fmt.Sprintf("help:%s:%s:%d:%s", a.helpEpoch, owner, expires, kind) }
	return discord.MessageCreate{Content: notice, Flags: discord.MessageFlagEphemeral, Embeds: []discord.Embed{p.Embed}, AllowedMentions: &discord.AllowedMentions{}, Components: []discord.LayoutComponent{
		discord.NewActionRow(discord.NewStringSelectMenu(id("guide"), "ガイドを選ぶ", guides...)),
		discord.NewActionRow(discord.NewStringSelectMenu(id("command"), "コマンドの詳細を調べる", commands...)),
	}}
}

func (a *App) helpCommand(e *events.ApplicationCommandInteractionCreate) {
	m := a.helpMessage(e.SlashCommandInteractionData().String("page"), e.User().ID.String(), time.Now().Add(helpTTL).Unix())
	a.logInteractionFailure(e.ID(), "interaction.help", e.CreateMessage(m))
}

func helpAccess(customID, epoch, owner string, now time.Time) (int64, string, bool) {
	p := strings.Split(customID, ":")
	if len(p) != 5 || p[0] != "help" || p[1] != epoch || epoch == "" || p[2] != owner || (p[4] != "guide" && p[4] != "command") {
		return 0, "", false
	}
	expires, err := strconv.ParseInt(p[3], 10, 64)
	return expires, p[4], err == nil && now.Unix() < expires && expires <= now.Add(helpTTL).Unix()
}

func (a *App) helpComponent(e *events.ComponentInteractionCreate) {
	fail := func() {
		a.logInteractionFailure(e.ID(), "interaction.help_expired", e.CreateMessage(discord.MessageCreate{Content: "この操作は利用できません。/helpを実行し直してください。", Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}}))
	}
	expires, kind, ok := helpAccess(e.Data.CustomID(), a.helpEpoch, e.User().ID.String(), time.Now())
	if !ok || e.GuildID() == nil || *e.GuildID() != a.config.GuildID {
		fail()
		return
	}
	data, ok := e.Data.(discord.StringSelectMenuInteractionData)
	if !ok || len(data.Values) != 1 || !strings.HasPrefix(data.Values[0], kind+":") {
		fail()
		return
	}
	m := a.helpMessage(data.Values[0], e.User().ID.String(), expires)
	a.logInteractionFailure(e.ID(), "interaction.help_update", e.UpdateMessage(discord.MessageUpdate{Content: &m.Content, Embeds: &m.Embeds, Components: &m.Components, AllowedMentions: &discord.AllowedMentions{}}))
}

func (a *App) onHelpAutocomplete(e *events.AutocompleteInteractionCreate) {
	if e.GuildID() == nil || *e.GuildID() != a.config.GuildID || e.Data.CommandName != "help" {
		return
	}
	defer a.observeOperation(e.ID(), "autocomplete")()
	a.logInteractionFailure(e.ID(), "interaction.help_autocomplete", e.AutocompleteResult(a.helpCatalog.Search(e.Data.String("page"))))
}
