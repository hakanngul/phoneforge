package config

import (
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", UserSettingsFile)
	if ReadSetting(p, KeyWDATeamID) != "" {
		t.Fatal("expected empty")
	}
	if err := WriteSetting(p, "other", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := WriteSetting(p, KeyWDATeamID, "ABCDE12345"); err != nil {
		t.Fatal(err)
	}
	if ReadSetting(p, KeyWDATeamID) != "ABCDE12345" || ReadSetting(p, "other") != "keep" {
		t.Fatal("values lost")
	}
	_ = WriteSetting(p, KeyWDATeamID, "")
	if ReadSetting(p, KeyWDATeamID) != "" || ReadSetting(p, "other") != "keep" {
		t.Fatal("remove failed")
	}
}
