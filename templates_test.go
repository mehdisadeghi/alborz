package alborz

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTranslationKeysExist(t *testing.T) {
	keyRe := regexp.MustCompile(`\{\{-?\s*\$?\.?\$?[A-Za-z.]*\bTf?\s+"([a-z0-9._]+)"`)
	used := map[string][]string{}
	err := filepath.WalkDir("templates", func(f string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(f) != ".html" {
			return err
		}
		b, err := os.ReadFile(f)
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = append(used[m[1]], f)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) == 0 {
		t.Fatal("no translation keys found; the pattern is wrong, not the templates")
	}

	locales, err := filepath.Glob("locales/*.toml")
	if err != nil || len(locales) == 0 {
		t.Fatal("no locales found")
	}
	for _, loc := range locales {
		b, err := os.ReadFile(loc)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for key, where := range used {
			if !strings.Contains(text, `"`+key+`"`) {
				t.Errorf("%s has no %q, used by %s", loc, key, where[0])
			}
		}
	}
}
