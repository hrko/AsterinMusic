package config

import (
	"strings"
	"testing"
)

func TestAuthorizationConfiguration(t *testing.T) {
	c, err := Read(setup(t, valid), lookup)
	if err != nil || c.Authorization.Preset != "collaborative" {
		t.Fatal("authorization defaults", err)
	}
	text := valid + `    authorization:
      preset: dj
      dj_role_ids: ["456"]
      policies:
        - id: forbid_stop
          policy: 'forbid(principal, action == Action::"playback.stop", resource);'
`
	c, err = Read(setup(t, text), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if c.Authorization.Preset != "dj" || len(c.Authorization.DJRoleIDs) != 1 || len(c.Authorization.Policies) != 1 {
		t.Fatal("missing authorization config")
	}
}

func TestAuthorizationConfigurationRejectsInvalid(t *testing.T) {
	for _, block := range []string{
		"      preset: unknown\n",
		"      preset: dj\n",
		"      dj_role_ids: [456]\n",
		"      typo: secret-value\n",
		"      policies:\n        - id: broken\n          policy: 'secret-value'\n",
		"      policies:\n        - id: broken\n          policy: 'permit(principal, action == Action::\"unknown\", resource);'\n",
	} {
		_, err := Read(setup(t, valid+"    authorization:\n"+block), lookup)
		if err == nil {
			t.Fatal("invalid authorization accepted")
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("policy content leaked")
		}
	}
}
