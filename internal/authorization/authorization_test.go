package authorization

import (
	"context"
	"fmt"
	"strings"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
)

func TestConfigurationLimits(t *testing.T) {
	text := `permit(principal, action, resource);`
	policies := make([]Policy, MaxPolicies)
	for i := range policies {
		policies[i] = Policy{ID: fmt.Sprintf("rule%d", i), Text: text}
	}
	if _, err := NewCedar(Config{Policies: policies}); err != nil {
		t.Fatal("policy count boundary", err)
	}
	if _, err := NewCedar(Config{Policies: append(policies, Policy{"extra", text})}); err == nil {
		t.Fatal("policy count exceeded")
	}
	for _, length := range []int{MaxPolicyBytes, MaxPolicyBytes + 1} {
		_, err := NewCedar(Config{Policies: []Policy{{"size", text + strings.Repeat(" ", length-len(text))}}})
		if (err == nil) != (length == MaxPolicyBytes) {
			t.Fatalf("policy bytes %d: %v", length, err)
		}
	}
	large := make([]Policy, MaxTotalPolicyBytes/MaxPolicyBytes)
	for i := range large {
		large[i] = Policy{fmt.Sprintf("large%d", i), text + strings.Repeat(" ", MaxPolicyBytes-len(text))}
	}
	if _, err := NewCedar(Config{Policies: large}); err != nil {
		t.Fatal("total bytes boundary", err)
	}
	if _, err := NewCedar(Config{Policies: append(large, Policy{"extra", text})}); err == nil {
		t.Fatal("total bytes exceeded")
	}
	roles := make([]string, MaxRoles)
	for i := range roles {
		roles[i] = fmt.Sprint(i + 10)
	}
	s, err := NewCedar(Config{DJRoleIDs: roles})
	if err != nil {
		t.Fatal(err)
	}
	r := sample()
	r.Principal.RoleIDs = roles
	if d, e := s.Authorize(context.Background(), r); e != nil || !d.Allowed {
		t.Fatal(d, e)
	}
	r.Principal.RoleIDs = append(roles, "999")
	if d, _ := s.Authorize(context.Background(), r); d.Allowed {
		t.Fatal("request role limit bypassed")
	}
	if _, err := NewCedar(Config{DJRoleIDs: r.Principal.RoleIDs}); err == nil {
		t.Fatal("config role limit bypassed")
	}
}

func BenchmarkService(b *testing.B) {
	for _, n := range []int{0, MaxPolicies} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			config := Config{Preset: "dj", DJRoleIDs: []string{"5"}}
			for i := 0; i < n; i++ {
				config.Policies = append(config.Policies, Policy{fmt.Sprintf("role%d", i), `permit(principal in Role::"1/5", action, resource) when {context.sameVoice};`})
			}
			s, err := NewCedar(config)
			if err != nil {
				b.Fatal(err)
			}
			r := sample()
			for i := 0; i < MaxRoles; i++ {
				r.Principal.RoleIDs = append(r.Principal.RoleIDs, fmt.Sprint(i+1))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				d, e := s.Authorize(context.Background(), r)
				if e != nil || !d.Allowed {
					b.Fatal(d, e)
				}
			}
		})
	}
}

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
