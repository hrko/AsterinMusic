package helpui

import (
	"fmt"
	"github.com/disgoorg/disgo/discord"
	"strings"
)

const Home = "guide:home"

type Page struct {
	ID, Name, Summary string
	Guide             bool
	Embed             discord.Embed
}
type Catalog struct{ pages []Page }

// New builds one immutable inventory for search, navigation and rendering.
// links maps root command names to Discord command IDs, and may be nil.
func New(commands []discord.ApplicationCommandCreate, links map[string]string) (*Catalog, error) {
	c := &Catalog{}
	known := map[string]bool{}
	for _, cmd := range commands {
		known[cmd.CommandName()] = true
	}
	for _, g := range Guides() {
		body := g.Body
		for strings.Contains(body, "{{") {
			start := strings.Index(body, "{{")
			end := strings.Index(body[start:], "}}")
			if end < 0 {
				return nil, fmt.Errorf("invalid guide reference")
			}
			name := body[start+2 : start+end]
			root := strings.Fields(name)[0]
			if !known[root] {
				return nil, fmt.Errorf("guide references unregistered command %s", root)
			}
			label := "`/" + name + "`"
			if id := links[root]; id != "" {
				label = "</" + name + ":" + id + ">"
			}
			body = body[:start] + label + body[start+end+2:]
		}
		c.pages = append(c.pages, Page{g.ID, g.Name, g.Summary, true, discord.Embed{Title: g.Name, Description: body, Color: 0x527DA8}})
	}
	refs := references()
	for _, command := range commands {
		cmd, ok := command.(discord.SlashCommandCreate)
		if !ok {
			return nil, fmt.Errorf("unsupported help command type")
		}
		ref, ok := refs[cmd.Name]
		if !ok {
			return nil, fmt.Errorf("missing reference for %s", cmd.Name)
		}
		e, err := referenceEmbed(cmd, ref)
		if err != nil {
			return nil, err
		}
		c.pages = append(c.pages, Page{"command:" + cmd.Name, "/" + cmd.Name, cmd.Description, false, e})
	}
	return c, nil
}

func (c *Catalog) Pages() []Page { return append([]Page(nil), c.pages...) }
func (c *Catalog) Find(id string) (Page, bool) {
	for _, p := range c.pages {
		if p.ID == id {
			return p, true
		}
	}
	return Page{}, false
}
func (c *Catalog) Search(query string) []discord.AutocompleteChoice {
	query = strings.ToLower(strings.TrimSpace(query))
	var out []discord.AutocompleteChoice
	for _, p := range c.pages {
		if !strings.Contains(strings.ToLower(p.Name+" "+p.Summary+" "+p.ID), query) {
			continue
		}
		kind := "コマンド："
		if p.Guide {
			kind = "ガイド："
		}
		out = append(out, discord.AutocompleteChoiceString{Name: kind + p.Name + " — " + p.Summary, Value: p.ID})
		if len(out) == 25 {
			break
		}
	}
	return out
}

func referenceEmbed(cmd discord.SlashCommandCreate, ref Reference) (discord.Embed, error) {
	e := discord.Embed{Title: "/" + cmd.Name, Description: cmd.Description, Color: 0x527DA8}
	syntax := "/" + cmd.Name
	var args, subs []string
	optional := false
	for _, opt := range cmd.Options {
		if sub, ok := opt.(discord.ApplicationCommandOptionSubCommand); ok {
			if len(sub.Options) > 0 {
				return e, fmt.Errorf("nested options need help support")
			}
			subs = append(subs, "/"+cmd.Name+" "+sub.Name)
			args = append(args, "- **"+sub.Name+"**："+sub.Description)
			continue
		}
		required := false
		details := ""
		switch o := opt.(type) {
		case discord.ApplicationCommandOptionString:
			required = o.Required
			for _, choice := range o.Choices {
				details += " · " + choice.Value + "（" + choice.Name + "）"
			}
		case discord.ApplicationCommandOptionInt:
			required = o.Required
			if o.MinValue != nil {
				details += fmt.Sprintf(" · 最小%d", *o.MinValue)
			}
			if o.MaxValue != nil {
				details += fmt.Sprintf(" · 最大%d", *o.MaxValue)
			}
		default:
			return e, fmt.Errorf("unsupported help option %s", opt.OptionName())
		}
		label := "任意"
		token := opt.OptionName() + ":値"
		if required {
			label = "必須"
		} else {
			token = "[" + token + "]"
			optional = true
		}
		syntax += " " + token
		if value := ref.Defaults[opt.OptionName()]; value != "" {
			details += " · 既定" + value
		}
		args = append(args, "- **"+opt.OptionName()+"** · "+label+details+"  \n  "+opt.OptionDescription())
	}
	if len(subs) > 0 {
		syntax = strings.Join(subs, "\n")
	}
	format := "```\n" + syntax + "\n```"
	if optional {
		format += "\n-# 角括弧内は省略できます。"
	}
	e.Fields = append(e.Fields, discord.EmbedField{Name: "書式", Value: format})
	if len(args) > 0 {
		name := "引数"
		if len(subs) > 0 {
			name = "サブコマンド"
		}
		e.Fields = append(e.Fields, discord.EmbedField{Name: name, Value: strings.Join(args, "\n")})
	}
	e.Fields = append(e.Fields, discord.EmbedField{Name: "動作", Value: ref.Behavior})
	var examples []string
	for _, ex := range ref.Examples {
		examples = append(examples, "- "+ex.Purpose+"\n  ```\n  "+ex.Command+"\n  ```")
	}
	conditions := ref.Conditions + "\nサーバーの権限設定により利用できる操作が異なります。"
	if cmd.Name != "help" {
		conditions += "音楽情報の表示には対象VCの閲覧権限が必要です。"
	}
	e.Fields = append(e.Fields, discord.EmbedField{Name: "使用例", Value: strings.Join(examples, "\n")}, discord.EmbedField{Name: "利用条件", Value: conditions})
	return e, nil
}
