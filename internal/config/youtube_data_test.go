package config

import "testing"

func TestYouTubeDataConfig(t *testing.T) {
	c, err := Read(setup(t, valid), lookup)
	if err != nil || c.YouTubeDataEnabled {
		t.Fatal("default")
	}
	for _, tc := range []struct {
		yaml string
		ok   bool
	}{
		{"enabled: true\n      api_key: test", true},
		{"enabled: true", false},
		{"enabled: false", true},
		{"enabled: 'true'", false},
		{"enabled: true\n      api_key: 123", false},
	} {
		_, err := Read(setup(t, valid+"sources:\n  youtube:\n    data_api:\n      "+tc.yaml+"\n"), lookup)
		if (err == nil) != tc.ok {
			t.Fatalf("config validation: %s", tc.yaml)
		}
	}
}
