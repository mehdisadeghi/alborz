package alborz

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Go resolves {{template}} names only at execution time.
func TestTemplateReferencesResolve(t *testing.T) {
	var files []string
	for _, glob := range templateGlobs {
		found, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		t.Fatal("no templates found; the glob is wrong, not the templates")
	}

	defineRe := regexp.MustCompile(`\{\{-?\s*define\s+"([^"]+)"`)
	useRe := regexp.MustCompile(`\{\{-?\s*template\s+"([^"]+)"`)

	defined := map[string]bool{}
	for _, f := range files {
		defined[filepath.Base(f)] = true
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range defineRe.FindAllStringSubmatch(string(b), -1) {
			defined[m[1]] = true
		}
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range useRe.FindAllStringSubmatch(string(b), -1) {
			if !defined[m[1]] {
				t.Errorf("%s calls template %q, which nothing defines", f, m[1])
			}
		}
	}
}

func TestTranslationKeysExist(t *testing.T) {
	var files []string
	for _, glob := range templateGlobs {
		found, _ := filepath.Glob(glob)
		files = append(files, found...)
	}

	keyRe := regexp.MustCompile(`\{\{-?\s*\$?\.?\$?[A-Za-z.]*\bTf?\s+"([a-z0-9._]+)"`)
	used := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = append(used[m[1]], f)
		}
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
