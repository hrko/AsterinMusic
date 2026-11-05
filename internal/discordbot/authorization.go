package discordbot

import (
	"context"
	"errors"
	"time"

	"asterinmusic/internal/authorization"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

var errPermission = errors.New("この操作は許可されていません。サーバーの権限設定を確認してください。")
var errPermissionFacts = errors.New("権限を確認できませんでした。少し待って操作し直してください。")

// authorizationTarget is a snapshot, not a capability. Callers must still check
// session/queue generations immediately before using the decision.
type authorizationTarget struct {
	guild, voice       snowflake.ID
	session, requester string
}

// authorizeTarget refreshes membership on every call, including the check after
// slow source work. Roles/channels are kept current by the Guilds Gateway intent.
// It must be called without holding a session lock.
func (a *App) authorizeTarget(ctx context.Context, user snowflake.ID, action authorization.Action, target authorizationTarget) error {
	if a.authorizer == nil || target.guild != a.config.GuildID {
		return errPermissionFacts
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	member, err := a.client.Rest.GetMember(target.guild, user, rest.WithCtx(ctx))
	if err != nil || member == nil || member.User.ID != user {
		return errPermissionFacts
	}
	member.GuildID = target.guild
	guild, ok := a.client.Caches.Guild(target.guild)
	if !ok {
		return errPermissionFacts
	}
	roles := make(map[snowflake.ID]discord.Permissions, len(member.RoleIDs)+1)
	for _, id := range append([]snowflake.ID{target.guild}, member.RoleIDs...) {
		role, found := a.client.Caches.Role(target.guild, id)
		if !found {
			return errPermissionFacts
		}
		roles[id] = role.Permissions
	}
	var overwrites discord.PermissionOverwrites
	if target.voice != 0 {
		channel, found := a.client.Caches.Channel(target.voice)
		if !found || channel.GuildID() != target.guild {
			return errPermissionFacts
		}
		overwrites = channel.PermissionOverwrites()
	}
	permissions, admin, ok := authorizationPermissions(*member, guild.OwnerID, roles, overwrites)
	if !ok {
		return errPermissionFacts
	}
	currentVoice := snowflake.ID(0)
	if state, found := a.client.Caches.VoiceState(target.guild, user); found && state.ChannelID != nil {
		currentVoice = *state.ChannelID
	}
	principal := authorization.Principal{GuildID: target.guild.String(), UserID: user.String(), Administrator: admin, Owner: guild.OwnerID == user}
	for _, id := range member.RoleIDs {
		principal.RoleIDs = append(principal.RoleIDs, id.String())
	}
	voice := ""
	if target.voice != 0 {
		voice = target.voice.String()
	}
	request := authorization.Request{Action: action, Principal: principal,
		Resource: authorization.Resource{GuildID: target.guild.String(), BotID: a.config.ApplicationID.String(), SessionID: target.session, VoiceID: voice, RequesterID: target.requester},
		Facts:    authorization.Facts{Complete: true, CanView: target.voice == 0 || permissions.Has(discord.PermissionViewChannel), InVoice: currentVoice != 0, SameVoice: currentVoice != 0 && (target.voice == 0 || currentVoice == target.voice)}}
	decision, err := a.authorizer.Authorize(ctx, request)
	if err != nil {
		return errPermissionFacts
	}
	if !decision.Allowed {
		return errPermission
	}
	return nil
}

// Discord overwrites apply in stages: everyone, aggregate roles, then member.
// In particular a role allow must not override a member-specific deny.
func authorizationPermissions(member discord.Member, owner snowflake.ID, roles map[snowflake.ID]discord.Permissions, overwrites discord.PermissionOverwrites) (discord.Permissions, bool, bool) {
	base, ok := roles[member.GuildID]
	if !ok {
		return 0, false, false
	}
	for _, id := range member.RoleIDs {
		p, found := roles[id]
		if !found {
			return 0, false, false
		}
		base |= p
	}
	admin := base.Has(discord.PermissionAdministrator)
	if admin || member.User.ID == owner {
		return discord.PermissionsAll, admin, true
	}
	if overwrite, found := overwrites.Role(member.GuildID); found {
		base = (base &^ overwrite.Deny) | overwrite.Allow
	}
	var allow, deny discord.Permissions
	for _, id := range member.RoleIDs {
		if id == member.GuildID {
			continue
		}
		if overwrite, found := overwrites.Role(id); found {
			allow |= overwrite.Allow
			deny |= overwrite.Deny
		}
	}
	base = (base &^ deny) | allow
	if overwrite, found := overwrites.Member(member.User.ID); found {
		base = (base &^ overwrite.Deny) | overwrite.Allow
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		base &= discord.PermissionViewChannel | discord.PermissionReadMessageHistory
	}
	return base, admin, true
}

func commandAction(d discord.SlashCommandInteractionData) authorization.Action {
	switch d.CommandName() {
	case "help":
		return authorization.HelpView
	case "history":
		return authorization.HistoryView
	case "play":
		if d.String("url") == "" {
			return authorization.PlaybackResume
		}
		return authorization.QueueAdd
	case "playnext":
		return authorization.QueueAddNext
	case "mix":
		return authorization.QueueAdd
	case "stop":
		return authorization.PlaybackStop
	case "skip":
		return authorization.PlaybackSkip
	case "queue":
		if d.String("action") == "clear" {
			return authorization.QueueClear
		}
		return authorization.QueueView
	case "nowplaying":
		return authorization.PlaybackView
	case "volume":
		if _, ok := d.OptInt("percent"); ok {
			return authorization.VolumeSet
		}
		return authorization.VolumeView
	case "autoplay":
		if d.SubCommandName != nil && *d.SubCommandName != "status" {
			return authorization.AutoplaySet
		}
		return authorization.AutoplayView
	default:
		return ""
	}
}
