package config

import "testing"

func TestMetadataLanguage(t *testing.T) {
	c, err := Read(setup(t, valid), lookup)
	if err != nil || c.YouTubeMetadataLanguage != "ja" {
		t.Fatal("default language")
	}
	for _, v := range []string{"ja", "en", "zh-TW", "provider_default", "auto"} {
		c, err := Read(setup(t, valid+"sources:\n  youtube:\n    metadata_language: "+v+"\n"), lookup)
		if err != nil || c.YouTubeMetadataLanguage != v {
			t.Fatal(v, err)
		}
	}
	for _, v := range []string{"''", "'ja;player_client=web'", "' JA '", "123"} {
		if _, err := Read(setup(t, valid+"sources:\n  youtube:\n    metadata_language: "+v+"\n"), lookup); err == nil {
			t.Fatal("invalid language accepted")
		}
	}
}
