package config

import "testing"

func TestDisconnectWhenIdle(t *testing.T) {
	for _, tc := range []struct {
		extra   string
		want    bool
		invalid bool
	}{
		{"", false, false},
		{"playback:\n  disconnect_when_idle: true\n", true, false},
		{"playback:\n  disconnect_when_idle: false\n", false, false},
		{"playback:\n  disconnect_when_idle: 'false'\n", false, true},
	} {
		c, err := Read(setup(t, valid+tc.extra), lookup)
		if (err != nil) != tc.invalid {
			t.Fatalf("unexpected error: %v", err)
		}
		if err == nil && c.DisconnectWhenIdle != tc.want {
			t.Fatal("incorrect idle policy")
		}
	}
}
