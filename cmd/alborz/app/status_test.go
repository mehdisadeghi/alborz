package app_test

import (
	"io/fs"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/locales"
)

// The settings name a segment by a key put together from its name, which no template check sees.
func TestEverySegmentIsNamedInEveryLanguage(t *testing.T) {
	a := Alborz(t)
	if len(a.Server.Status) == 0 {
		t.Fatal("no segments mounted; the check is vacuous")
	}
	files, err := fs.Glob(locales.FS, "*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no locales: %v", err)
	}
	for _, name := range files {
		text, err := fs.ReadFile(locales.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range a.Server.Status {
			if !strings.Contains(string(text), `"`+s.Name+`.status"`) {
				t.Errorf("%s does not name the segment %s", name, s.Name)
			}
		}
	}
}
