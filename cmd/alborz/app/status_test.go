package app_test

import (
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/cmd/alborz/app"
	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/locales"
	"git.mehdix.org/alborz/web"
)

// The settings name a segment or an add-on by a key put together from its name, which no template check sees.
func TestEverySegmentAndAddonIsNamedInEveryLanguage(t *testing.T) {
	// The example collection is itself a collection, so the add-on that needs one is mounted too.
	a := Alborz(t, MOTD("../../../addons/motd"))
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
		for _, addon := range a.Server.Addons {
			if !strings.Contains(string(text), `"`+addon.Name+`.addon"`) {
				t.Errorf("%s does not name the add-on %s", name, addon.Name)
			}
		}
	}
}

// The settings name a theme by a key put together from its file name, which no template check sees.
func TestEveryThemeIsNamedInEveryLanguage(t *testing.T) {
	themes, err := fs.Glob(alborz.Templates, "templates/assets/themes/*.css")
	if err != nil || len(themes) == 0 {
		t.Fatalf("no themes: %v", err)
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
		for _, theme := range themes {
			key := "theme." + strings.TrimSuffix(path.Base(theme), ".css")
			if !strings.Contains(string(text), `"`+key+`"`) {
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
			Upstreams: []string{"imap://127.0.0.1:1"}}}, MOTD: dir}
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
