// Package authorization contains adoption probes, not the Bot's authorizer.
package authorization

import (
	"fmt"
	"strings"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	expast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

const probeSchema = `entity User; entity Session;
action "skip" appliesTo { principal: User, resource: Session, context: {sameVC: Bool, dj: Bool} };`

func policies(t testing.TB, text string) *cedar.PolicySet {
	t.Helper()
	ps, err := cedar.NewPolicySetFromBytes("probe", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func request() cedar.Request {
	return cedar.Request{Principal: cedar.NewEntityUID("User", "guild/user"), Action: cedar.NewEntityUID("Action", "skip"), Resource: cedar.NewEntityUID("Session", "guild/bot/vc"), Context: cedar.NewRecord(cedar.RecordMap{"sameVC": cedar.True, "dj": cedar.False})}
}

func TestDecisionSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, policy           string
		allow, evaluationError bool
	}{
		{"default deny", "", false, false},
		{"collaborative", `permit(principal, action, resource) when {context.sameVC};`, true, false},
		{"dj without role", `permit(principal, action, resource) when {context.sameVC && context.dj};`, false, false},
		{"additional permit", `permit(principal, action, resource) when {context.dj}; permit(principal == User::"guild/user", action, resource);`, true, false},
		{"forbid overrides", `permit(principal, action, resource); forbid(principal, action, resource);`, false, false},
		{"forbid first", `forbid(principal, action, resource); permit(principal, action, resource);`, false, false},
		// Deliberately bypass schema validation to verify the required runtime backstop.
		{"broken forbid", `permit(principal, action, resource); forbid(principal, action, resource) when {context.missing};`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, diag := cedar.Authorize(policies(t, tc.policy), nil, request())
			if (decision == cedar.Allow) != tc.allow || (len(diag.Errors) > 0) != tc.evaluationError {
				t.Fatalf("decision=%v diagnostic=%+v", decision, diag)
			}
			safeAllow := decision == cedar.Allow && len(diag.Errors) == 0
			if tc.evaluationError && safeAllow {
				t.Fatal("evaluation error must deny at application boundary")
			}
		})
	}
}

func TestSchemaValidation(t *testing.T) {
	var s schema.Schema
	if err := s.UnmarshalCedar([]byte(probeSchema)); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	validator := validate.New(resolved)
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"valid", `permit(principal, action == Action::"skip", resource) when {context.sameVC};`, true},
		{"unknown action", `permit(principal, action == Action::"skpi", resource);`, false},
		{"unknown attribute", `permit(principal, action, resource) when {context.missing};`, false},
		{"wrong type", `permit(principal, action, resource) when {context.dj > 1};`, false},
		{"wrong resource", `permit(principal, action, resource is User);`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := policies(t, tc.text)
			for id, p := range ps.All() {
				if err := validator.Policy(string(id), (*expast.Policy)(p.AST())); (err == nil) != tc.valid {
					t.Fatalf("valid=%v error=%v", tc.valid, err)
				}
			}
		})
	}
	if _, err := cedar.NewPolicySetFromBytes("invalid", []byte("permit(")); err == nil {
		t.Fatal("invalid syntax accepted")
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	ps := policies(t, `permit(principal, action, resource) when {context.sameVC};`)
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			for j := 0; j < 100; j++ {
				d, diag := cedar.Authorize(ps, nil, request())
				if d != cedar.Allow || len(diag.Errors) > 0 {
					t.Fatal(d, diag)
				}
			}
		})
	}
}

func BenchmarkEvaluation(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ps := policies(b, strings.Repeat(`permit(principal, action, resource) when {context.sameVC};`, n))
			req := request()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				d, diag := cedar.Authorize(ps, nil, req)
				if d != cedar.Allow || len(diag.Errors) > 0 {
					b.Fatal(d, diag)
				}
			}
		})
	}
}
