package config

import "testing"

func TestHistoryRetention(t *testing.T) {
	for _, tc := range []struct {
		extra   string
		want    int
		invalid bool
	}{
		{"", 10000, false},
		{"history:\n  limit_per_voice: 50000\n", 50000, false},
		{"history:\n  limit_per_voice: 0\n", 0, true},
		{"history:\n  limit_per_voice: 1000001\n", 0, true},
		{"history:\n  limit_per_voice: '100'\n", 0, true},
	} {
		c, err := Read(setup(t, valid+tc.extra), lookup)
		if (err != nil) != tc.invalid {
			t.Fatal(err)
		}
		if err == nil && c.HistoryLimit != tc.want {
			t.Fatal(c.HistoryLimit)
		}
	}
}
