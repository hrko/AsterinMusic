// Package authorization defines the application's engine-independent access decisions.
package authorization

import (
	"context"
	"errors"
)

const (
	PresetCollaborative     = "collaborative"
	PresetDJControl         = "dj_control"
	PresetDJControlSelfSkip = "dj_control_self_skip"
	PresetDJOnly            = "dj_only"
)

// NormalizePreset applies the default and rejects unknown or retired names.
func NormalizePreset(name string) (string, error) {
	switch name {
	case "":
		return PresetCollaborative, nil
	case PresetCollaborative, PresetDJControl, PresetDJControlSelfSkip, PresetDJOnly:
		return name, nil
	default:
		return "", errors.New("unknown authorization preset")
	}
}

type Action string

const (
	HelpView         Action = "help.view"
	QueueAdd         Action = "queue.add"
	QueueAddNext     Action = "queue.add_next"
	PlaybackResume   Action = "playback.resume"
	PlaybackStop     Action = "playback.stop"
	PlaybackSkip     Action = "playback.skip"
	QueueClear       Action = "queue.clear"
	QueueView        Action = "queue.view"
	PlaybackView     Action = "playback.view"
	VolumeView       Action = "volume.view"
	VolumeSet        Action = "volume.set"
	AutoplayView     Action = "autoplay.view"
	AutoplaySet      Action = "autoplay.set"
	HistoryView      Action = "history.view"
	HistoryReadd     Action = "history.readd"
	HistoryReaddNext Action = "history.readd_next"
)

func Actions() []Action {
	return []Action{HelpView, QueueAdd, QueueAddNext, PlaybackResume, PlaybackStop, PlaybackSkip, QueueClear, QueueView, PlaybackView, VolumeView, VolumeSet, AutoplayView, AutoplaySet, HistoryView, HistoryReadd, HistoryReaddNext}
}

// Facts must come from trusted Discord/application state, never interaction parameters.
// Complete is false if any required fact could not be obtained. No network I/O occurs in evaluation.
type Request struct {
	Action    Action
	Principal Principal
	Resource  Resource
	Facts     Facts
}
type Principal struct {
	GuildID, UserID      string
	RoleIDs              []string
	Administrator, Owner bool
}
type Resource struct {
	GuildID, BotID, SessionID, VoiceID, RequesterID string
	Automatic                                       bool
}
type Facts struct{ Complete, CanView, SameVoice, InVoice, ScopeUnknown bool }

type Reason string

const (
	Allowed          Reason = "allowed"
	NotPermitted     Reason = "not_permitted"
	InvalidFacts     Reason = "invalid_facts"
	EvaluationFailed Reason = "evaluation_failed"
)

type Decision struct {
	Allowed   bool
	Reason    Reason
	PolicyIDs []string
	Version   string
}
type Service interface {
	Authorize(context.Context, Request) (Decision, error)
}

type Policy struct{ ID, Text string }
type Config struct {
	Preset    string
	DJRoleIDs []string
	Policies  []Policy
}
