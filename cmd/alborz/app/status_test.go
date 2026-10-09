//go:build !noaddons && !nomotd

package app_test

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/cmd/alborz/app"
	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/locale"
	"git.mehdix.org/alborz/web"
)

// The settings name a segment or an add-on by a key put together from its name, which no template check sees.
func TestEverySegmentAndAddonIsNamedInEveryLanguage(t *testing.T) {
	// The example collection is itself a collection, so the add-on that needs one is mounted too.
	a := Alborz(t, MOTD("../../../addons/motd"))
	if len(a.Server.Status) == 0 {
		t.Fatal("no segments mounted; the check is vacuous")
	}
	for _, name := range locale.Languages {
		for _, s := range a.Server.Status {
			key := s.Name + ".status"
			if locale.T(name, key) == key {
				t.Errorf("%s does not name the segment %s", name, s.Name)
			}
		}
		for _, addon := range a.Server.Addons {
			key := addon.Name + ".addon"
			if locale.T(name, key) == key {
				t.Errorf("%s does not name the add-on %s", name, addon.Name)
			}
		}
	}
}

// The settings name a theme by a key put together from its file name, which no template check sees.
func TestEveryThemeIsNamedInEveryLanguage(t *testing.T) {
	a := Alborz(t)
	themes := a.Server.Themes()
	if len(themes) == 0 {
		t.Fatal("no themes")
	}
	for _, name := range locale.Languages {
		for _, theme := range themes {
			key := "theme." + theme.Name
			if locale.T(name, key) == key {
				t.Errorf("%s does not name the theme %s", name, key)
			}
		}
	}
}

// A deployment that named a directory of texts asked for them; one alborz cannot read stops it.
func TestAnUnreadableMOTDStopsStartup(t *testing.T) {
	good, bad := t.TempDir(), t.TempDir()
	for dir, body := range map[string]string{good: "language = \"en\"\n", bad: ""} {
		if err := os.WriteFile(filepath.Join(dir, "en.toml"), []byte(body+"[[texts]]\ntext = \"x\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := func(dir string) error {
		options := app.Options{Options: web.Options{Options: alborz.Options{LoginKey: "YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=", DataDir: t.TempDir(),
			Upstreams: []string{"imap://127.0.0.1:1"}}}, Addons: map[string]any{"motd": dir}}
		s, _, err := app.New(log.New(io.Discard, "", 0), options)
		if err == nil {
			s.Close()
		}
		return err
	}
	if err := start(good); err != nil {
		t.Fatalf("alborz did not start with a collection: %v", err)
	}
	for _, dir := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir(), bad} {
		if start(dir) == nil {
			t.Errorf("alborz started with -motd %s", dir)
		}
	}
}
