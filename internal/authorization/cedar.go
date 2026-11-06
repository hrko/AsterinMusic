package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	expast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

const MaxPolicies = 100
const MaxPolicyBytes = 8192
const MaxTotalPolicyBytes = 65536
const MaxRoles = 250

type cedarService struct {
	policies  *cedar.PolicySet
	validator *validate.Validator
	djRoles   []string
	version   string
}

// NewCedar compiles a complete immutable policy snapshot. Errors never include policy text.
func NewCedar(c Config) (Service, error) {
	var err error
	c.Preset, err = NormalizePreset(c.Preset)
	if err != nil {
		return nil, err
	}
	if len(c.Policies) > MaxPolicies || len(c.DJRoleIDs) > MaxRoles {
		return nil, errors.New("authorization configuration exceeds limits")
	}
	for _, id := range c.DJRoleIDs {
		if !validID(id) {
			return nil, errors.New("invalid DJ role ID")
		}
	}
	if c.Preset != PresetCollaborative && len(c.DJRoleIDs) == 0 {
		return nil, errors.New("DJ preset requires at least one role")
	}
	var schemaText strings.Builder
	schemaText.WriteString(`entity Role; entity User in [Role] { administrator: Bool, owner: Bool }; entity Session { requester: String, entry: String };`)
	for _, action := range Actions() {
		fmt.Fprintf(&schemaText, `action %q appliesTo {principal: User, resource: Session, context: {sameVoice: Bool, inVoice: Bool, dj: Bool, ownTrack: Bool}};`, action)
	}
	var s schema.Schema
	if err := s.UnmarshalCedar([]byte(schemaText.String())); err != nil {
		return nil, errors.New("invalid built-in authorization schema")
	}
	resolved, err := s.Resolve()
	if err != nil {
		return nil, errors.New("unresolved authorization schema")
	}
	service := &cedarService{policies: cedar.NewPolicySet(), validator: validate.New(resolved), djRoles: slices.Clone(c.DJRoleIDs)}
	base := []Policy{
		{"builtin.read", `permit(principal, action in [Action::"help.view", Action::"queue.view", Action::"playback.view", Action::"volume.view", Action::"autoplay.view"], resource);`},
		{"builtin.history", `permit(principal, action == Action::"history.view", resource) when {context.inVoice && context.sameVoice};`},
	}
	condition := `context.inVoice && context.sameVoice`
	addCondition := condition
	if c.Preset == PresetDJOnly {
		addCondition += ` && (context.dj || principal.administrator || principal.owner)`
	}
	base = append(base, Policy{"builtin.add", `permit(principal, action in [Action::"queue.add", Action::"queue.add_next", Action::"history.readd", Action::"history.readd_next"], resource) when {` + addCondition + `};`})
	if c.Preset != PresetCollaborative {
		condition += ` && (context.dj || principal.administrator || principal.owner)`
	}
	base = append(base, Policy{"builtin.control", `permit(principal, action in [Action::"playback.resume", Action::"playback.stop", Action::"playback.skip", Action::"queue.clear", Action::"volume.set", Action::"autoplay.set"], resource) when {` + condition + `};`})
	editCondition := condition
	if c.Preset == PresetDJControl || c.Preset == PresetDJControlSelfSkip {
		editCondition = `context.inVoice && context.sameVoice && (context.ownTrack || context.dj || principal.administrator || principal.owner)`
	}
	base = append(base, Policy{"builtin.queue_edit", `permit(principal, action in [Action::"queue.remove", Action::"queue.reorder"], resource) when {` + editCondition + `};`})
	if c.Preset == PresetDJControlSelfSkip {
		base = append(base, Policy{"builtin.self_skip", `permit(principal, action == Action::"playback.skip", resource) when {context.inVoice && context.sameVoice && context.ownTrack};`})
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%v\x00", c.Preset, c.DJRoleIDs)
	total := 0
	for i, p := range append(base, c.Policies...) {
		if i >= len(base) {
			total += len(p.Text)
			if !validPolicyID(p.ID) || strings.HasPrefix(p.ID, "builtin.") || len(p.Text) > MaxPolicyBytes || total > MaxTotalPolicyBytes {
				return nil, errors.New("invalid authorization policy or size limit")
			}
		}
		if service.policies.Get(cedar.PolicyID(p.ID)) != nil {
			return nil, errors.New("duplicate authorization policy ID")
		}
		var policy cedar.Policy
		if err := policy.UnmarshalCedar([]byte(p.Text)); err != nil {
			return nil, fmt.Errorf("authorization policy %s: syntax error", p.ID)
		}
		if err := service.validator.Policy(p.ID, (*expast.Policy)(policy.AST())); err != nil {
			return nil, fmt.Errorf("authorization policy %s: schema validation failed", p.ID)
		}
		service.policies.Add(cedar.PolicyID(p.ID), &policy)
		fmt.Fprintf(hash, "%s\x00%s\x00", p.ID, p.Text)
	}
	service.version = hex.EncodeToString(hash.Sum(nil))[:16]
	return service, nil
}

func validID(id string) bool {
	n, e := strconv.ParseUint(id, 10, 64)
	return e == nil && n != 0 && strconv.FormatUint(n, 10) == id
}
func validPolicyID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (s *cedarService) Authorize(ctx context.Context, r Request) (Decision, error) {
	result := Decision{Reason: InvalidFacts, Version: s.version}
	if r.Facts.ScopeUnknown && !r.Principal.Administrator && !r.Principal.Owner {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !r.Facts.Complete || !r.Facts.CanView || !validID(r.Principal.UserID) || !validID(r.Principal.GuildID) || r.Principal.GuildID != r.Resource.GuildID || !validID(r.Resource.BotID) || len(r.Resource.SessionID) > 128 || len(r.Resource.EntryID) > 128 || !slices.Contains(Actions(), r.Action) || len(r.Principal.RoleIDs) > MaxRoles {
		return result, nil
	}
	if r.Resource.VoiceID != "" && !validID(r.Resource.VoiceID) {
		return result, nil
	}
	if r.Resource.RequesterID != "" && !validID(r.Resource.RequesterID) {
		return result, nil
	}
	guild := r.Principal.GuildID
	user := cedar.NewEntityUID("User", cedar.String(guild+"/"+r.Principal.UserID))
	target := cedar.NewEntityUID("Session", cedar.String(guild+"/"+r.Resource.BotID+"/"+r.Resource.SessionID+"/"+r.Resource.VoiceID))
	entities := cedar.EntityMap{}
	var parents []cedar.EntityUID
	dj := false
	for _, id := range r.Principal.RoleIDs {
		if !validID(id) {
			return result, nil
		}
		uid := cedar.NewEntityUID("Role", cedar.String(guild+"/"+id))
		parents = append(parents, uid)
		entities[uid] = cedar.Entity{UID: uid}
		dj = dj || slices.Contains(s.djRoles, id)
	}
	entities[user] = cedar.Entity{UID: user, Parents: cedar.NewEntityUIDSet(parents...), Attributes: cedar.NewRecord(cedar.RecordMap{"administrator": cedar.Boolean(r.Principal.Administrator), "owner": cedar.Boolean(r.Principal.Owner)})}
	entities[target] = cedar.Entity{UID: target, Attributes: cedar.NewRecord(cedar.RecordMap{"requester": cedar.String(r.Resource.RequesterID), "entry": cedar.String(r.Resource.EntryID)})}
	req := cedar.Request{Principal: user, Action: cedar.NewEntityUID("Action", cedar.String(r.Action)), Resource: target, Context: cedar.NewRecord(cedar.RecordMap{"sameVoice": cedar.Boolean(r.Facts.SameVoice), "inVoice": cedar.Boolean(r.Facts.InVoice), "dj": cedar.Boolean(dj), "ownTrack": cedar.Boolean(!r.Resource.Automatic && r.Resource.RequesterID != "" && r.Resource.RequesterID == r.Principal.UserID)})}
	if s.validator.Request(req) != nil || s.validator.Entities(entities) != nil {
		return result, errors.New("authorization facts failed schema validation")
	}
	decision, diag := cedar.Authorize(s.policies, entities, req)
	if len(diag.Errors) > 0 {
		result.Reason = EvaluationFailed
		return result, errors.New("authorization policy evaluation failed")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Reason = NotPermitted
	result.Allowed = decision == cedar.Allow
	if result.Allowed {
		result.Reason = Allowed
	}
	for _, reason := range diag.Reasons {
		result.PolicyIDs = append(result.PolicyIDs, string(reason.PolicyID))
	}
	slices.Sort(result.PolicyIDs)
	return result, nil
}
