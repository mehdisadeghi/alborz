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
	scans := []struct {
		root, ext string
		key       *regexp.Regexp
	}{
		{"templates", ".html", regexp.MustCompile(`\{\{-?\s*\$?\.?\$?[A-Za-z.]*\bTf?\s+"([a-z0-9._]+)"`)},
		// A key handed to the HTTP layer in Go, such as a not-found page's, is shown as the templates' are.
		{".", ".go", regexp.MustCompile(`\b(?:Tf?|Plural|NotFound)\((?:[\w.]+, )?"([a-z0-9._]+)"\s*[,)]`)},
	}
	used := map[string][]string{}
	for _, scan := range scans {
		found := 0
		err := filepath.WalkDir(scan.root, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if f == scan.root {
					return nil
				}
				// Hidden directories hold worktrees, and a nested module is not this one.
				if _, err := os.Stat(filepath.Join(f, "go.mod")); err == nil || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(f) != scan.ext || strings.HasSuffix(f, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(f)
			for _, m := range scan.key.FindAllStringSubmatch(string(b), -1) {
				used[m[1]] = append(used[m[1]], f)
				found++
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if found == 0 {
			t.Fatalf("no translation keys found in %s files; the pattern is wrong, not the code", scan.ext)
		}
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
