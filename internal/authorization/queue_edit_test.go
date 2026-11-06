package authorization

import (
	"context"
	"fmt"
	"testing"
)

func TestQueueEditPresets(t *testing.T) {
	for _, preset := range []string{PresetCollaborative, PresetDJControl, PresetDJControlSelfSkip, PresetDJOnly} {
		for _, action := range []Action{QueueRemove, QueueReorder} {
			for _, own := range []bool{false, true} {
				for _, kind := range []string{"participant", "dj", "admin", "owner"} {
					t.Run(fmt.Sprintf("%s/%s/own=%v/%s", preset, action, own, kind), func(t *testing.T) {
						s, err := NewCedar(Config{Preset: preset, DJRoleIDs: []string{"5"}})
						if err != nil {
							t.Fatal(err)
						}
						r := sample()
						r.Action = action
						r.Principal.RoleIDs = nil
						r.Principal.Administrator, r.Principal.Owner = kind == "admin", kind == "owner"
						if kind == "dj" {
							r.Principal.RoleIDs = []string{"5"}
						}
						r.Resource.RequesterID = "99"
						if own {
							r.Resource.RequesterID = r.Principal.UserID
						}
						want := kind != "participant" || preset == PresetCollaborative || (own && preset != PresetDJOnly)
						d, err := s.Authorize(context.Background(), r)
						if err != nil || d.Allowed != want {
							t.Fatal(d, err, want)
						}
						r.Facts.SameVoice = false
						if d, _ := s.Authorize(context.Background(), r); d.Allowed {
							t.Fatal("remote edit allowed by preset")
						}
					})
				}
			}
		}
	}
}

func TestQueueEditIndependentForbid(t *testing.T) {
	s, err := NewCedar(Config{Policies: []Policy{{ID: "deny_remove", Text: `forbid(principal, action == Action::"queue.remove", resource);`}}})
	if err != nil {
		t.Fatal(err)
	}
	r := sample()
	r.Principal.Owner = true
	r.Action = QueueRemove
	if d, _ := s.Authorize(context.Background(), r); d.Allowed {
		t.Fatal("owner bypassed forbid")
	}
	r.Action = QueueReorder
	if d, err := s.Authorize(context.Background(), r); err != nil || !d.Allowed {
		t.Fatal(d, err)
	}
	r.Facts.Complete = false
	if d, _ := s.Authorize(context.Background(), r); d.Allowed {
		t.Fatal("incomplete facts allowed")
	}
}
