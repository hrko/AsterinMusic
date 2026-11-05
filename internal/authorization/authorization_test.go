package authorization

import (
	"context"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
)

func TestRuntimeErrorFailsClosed(t *testing.T) {
	service, err := NewCedar(Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a validator regression: even a broken forbid must not allow access.
	var broken cedar.Policy
	if err := broken.UnmarshalCedar([]byte(`forbid(principal, action, resource) when {context.missing};`)); err != nil {
		t.Fatal(err)
	}
	service.(*cedarService).policies.Add("broken", &broken)
	d, err := service.Authorize(context.Background(), sample())
	if err == nil || d.Allowed || d.Reason != EvaluationFailed {
		t.Fatal(d, err)
	}
}

func TestConcurrentService(t *testing.T) {
	s, err := NewCedar(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for range 32 {
		t.Run("concurrent", func(t *testing.T) {
			t.Parallel()
			for range 100 {
				d, err := s.Authorize(context.Background(), sample())
				if err != nil || !d.Allowed {
					t.Fatal(d, err)
				}
			}
		})
	}
}

func sample() Request {
	return Request{Action: PlaybackSkip, Principal: Principal{GuildID: "1", UserID: "2"}, Resource: Resource{GuildID: "1", BotID: "3", SessionID: "session", VoiceID: "4"}, Facts: Facts{Complete: true, CanView: true, SameVoice: true, InVoice: true}}
}
func TestPresets(t *testing.T) {
	for _, preset := range []string{"collaborative", "dj"} {
		svc, err := NewCedar(Config{Preset: preset, DJRoleIDs: []string{"5"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range Actions() {
			r := sample()
			r.Action = a
			got, err := svc.Authorize(context.Background(), r)
			want := preset == "collaborative" || a == HelpView || a == QueueAdd || a == QueueAddNext || a == QueueView || a == PlaybackView || a == VolumeView || a == AutoplayView || a == HistoryView || a == HistoryReadd || a == HistoryReaddNext
			if err != nil || got.Allowed != want {
				t.Errorf("%s %s: %+v %v", preset, a, got, err)
			}
		}
		for _, kind := range []string{"role", "administrator", "owner"} {
			r := sample()
			switch kind {
			case "role":
				r.Principal.RoleIDs = []string{"6", "5"}
			case "administrator":
				r.Principal.Administrator = true
			case "owner":
				r.Principal.Owner = true
			}
			d, e := svc.Authorize(context.Background(), r)
			if e != nil || !d.Allowed {
				t.Fatalf("%s %s: %+v %v", preset, kind, d, e)
			}
		}
	}
}
func TestFixedBoundaries(t *testing.T) {
	svc, err := NewCedar(Config{Policies: []Policy{{"all", `permit(principal, action, resource);`}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Request){func(r *Request) { r.Principal.GuildID = "9" }, func(r *Request) { r.Facts.Complete = false }, func(r *Request) { r.Facts.CanView = false }, func(r *Request) { r.Action = "unknown" }, func(r *Request) { r.Principal.RoleIDs = []string{"1/5"} }} {
		r := sample()
		mutate(&r)
		d, _ := svc.Authorize(context.Background(), r)
		if d.Allowed {
			t.Fatal("fixed boundary bypassed")
		}
	}
}
func TestExtraPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		allow      bool
	}{
		{"own track", `permit(principal == User::"1/2", action == Action::"playback.skip", resource) when {resource.requester == "2"};`, true},
		{"guild role", `permit(principal in Role::"1/6", action == Action::"playback.skip", resource);`, true},
		{"other guild role", `permit(principal in Role::"9/6", action == Action::"playback.skip", resource);`, false},
		{"deny admin", `forbid(principal, action == Action::"queue.add", resource);`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := NewCedar(Config{Preset: "dj", DJRoleIDs: []string{"5"}, Policies: []Policy{{"custom", tc.text}}})
			if e != nil {
				t.Fatal(e)
			}
			r := sample()
			r.Principal.RoleIDs = []string{"6"}
			r.Resource.RequesterID = "2"
			if tc.name == "deny admin" {
				r.Action = QueueAdd
				r.Principal.Administrator = true
			}
			d, e := s.Authorize(context.Background(), r)
			if e != nil || d.Allowed != tc.allow {
				t.Fatal(d, e)
			}
		})
	}
}
func TestInvalidConfiguration(t *testing.T) {
	for _, c := range []Config{{Preset: "bad"}, {Preset: "dj"}, {DJRoleIDs: []string{"x"}}, {Policies: []Policy{{"builtin.override", `permit(principal, action, resource);`}}}, {Policies: []Policy{{"bad", `permit(principal, action == Action::"typo", resource);`}}}, {Policies: []Policy{{"bad", `permit(principal, action, resource) when {context.missing};`}}}} {
		if _, err := NewCedar(c); err == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
}
