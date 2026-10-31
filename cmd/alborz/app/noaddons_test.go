//go:build noaddons

package app_test

import (
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestCoreBuildOmitsAddonFeatures(t *testing.T) {
	a := Alborz(t)
	if len(a.Server.Addons) != 0 {
		t.Fatalf("core build offers %d add-ons", len(a.Server.Addons))
	}
	for _, segment := range a.Server.Status {
		if segment.Name == "moon" || segment.Name == "motd" || segment.Name == "weather" {
			t.Errorf("core build offers %s", segment.Name)
		}
	}
	for _, route := range a.Server.Routes() {
		for _, prefix := range []string{"moon.", "motd.", "season.", "weather."} {
			if strings.HasPrefix(route.Name, prefix) {
				t.Errorf("core build mounted %s", route.Name)
			}
		}
	}
	for _, theme := range a.Server.Themes() {
		for _, seasonal := range []string{"spring", "summer", "autumn", "winter"} {
			if theme.Name == seasonal {
				t.Errorf("core build offers %s theme", seasonal)
			}
		}
	}
}
