package alborz

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every {{ template "x" }} must name a template that exists. Go resolves
// these at execution time, so a define renamed or removed compiles, and
// tests that never render that page pass - the page just answers 500 to
// whoever opens it. This is the cheapest check that would have caught
// it, and it needs neither a server nor data.
func TestTemplateReferencesResolve(t *testing.T) {
	files, err := filepath.Glob("themes/*/*.html")
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := filepath.Glob("plugins/*/public/*.html")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, plugins...)
	if len(files) == 0 {
		t.Fatal("no templates found; the glob is wrong, not the templates")
	}

	defineRe := regexp.MustCompile(`\{\{-?\s*define\s+"([^"]+)"`)
	useRe := regexp.MustCompile(`\{\{-?\s*template\s+"([^"]+)"`)

	defined := map[string]bool{}
	for _, f := range files {
		// A file is itself a template, named by its base.
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

// Every {{$.T "key"}} and {{.T "key"}} must name a string every locale
// carries. A missing key renders as nothing, so a control silently
// loses its label in one language and no page fails.
func TestTranslationKeysExist(t *testing.T) {
	files, _ := filepath.Glob("themes/*/*.html")
	plugins, _ := filepath.Glob("plugins/*/public/*.html")
	files = append(files, plugins...)

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
