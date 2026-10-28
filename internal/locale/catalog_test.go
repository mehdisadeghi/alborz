package locale_test

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"git.mehdix.org/alborz/internal/locale"
	"github.com/BurntSushi/toml"
)

// Translation edits must preserve the arguments that callers supply. Singular
// plurals may spell out the count, but must still retain every other argument.
func TestCatalogs(t *testing.T) {
	dirs, err := filepath.Glob("../../addons/*/locales")
	if err != nil {
		t.Fatal(err)
	}
	dirs = append(dirs, "../../locales")
	for _, dir := range dirs {
		t.Run(dir, func(t *testing.T) {
			english := readCatalog(t, filepath.Join(dir, "en.toml"))
			for _, lang := range locale.Languages {
				t.Run(lang, func(t *testing.T) {
					catalog := readCatalog(t, filepath.Join(dir, lang+".toml"))
					for key := range english {
						if _, ok := catalog[key]; !ok {
							t.Errorf("missing %s", key)
						}
					}
					for key, value := range catalog {
						source, ok := english[key]
						if !ok {
							t.Errorf("unknown %s", key)
							continue
						}
						forms := catalogForms(t, key, value)
						sourceForms := catalogForms(t, key, source)
						if _, plural := forms["other"]; plural != (sourceForms["other"] != "") {
							t.Errorf("%s: singular/plural structure differs from English", key)
						}
						for form := range sourceForms {
							if _, ok := forms[form]; !ok {
								t.Errorf("%s: missing plural form %s", key, form)
							}
						}
						for form, text := range forms {
							if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsFunc(text, unicode.IsControl) {
								t.Errorf("%s/%s: empty, invalid UTF-8, or control characters", key, form)
							}
							sourceText, ok := sourceForms[form]
							if !ok {
								sourceText = sourceForms["other"]
							}
							got := formatArguments(t, key, text)
							want := formatArguments(t, key, sourceText)
							// Different languages can spell out "one" independently.
							if form == "one" {
								if got[1] == "d" || got[1] == "" {
									delete(got, 1)
								}
								if want[1] == "d" || want[1] == "" {
									delete(want, 1)
								}
							}
							if !reflect.DeepEqual(got, want) {
								t.Errorf("%s/%s: arguments %v, want %v", key, form, got, want)
							}
						}
					}
				})
			}
		})
	}
}

// Persian and French "one" plural categories include zero: spelling out "one"
// would report a successful change even when no item changed.
func TestZeroCountsInOneCategory(t *testing.T) {
	for _, lang := range []string{"fa", "fr"} {
		for _, tc := range []struct {
			key  string
			args []any
		}{
			{"notice.deleted", []any{0}},
			{"notice.imported", []any{0, "Inbox"}},
			{"aside.folderholds", []any{0, 0}},
			{"mail.status.unread", []any{0}},
		} {
			t.Run(lang+"/"+tc.key, func(t *testing.T) {
				got := locale.Tf(lang, tc.key, tc.args...)
				if !strings.Contains(got, locale.Num(lang, 0)) || strings.Contains(got, "یک") || strings.Contains(got, "%!") {
					t.Fatalf("zero items reported as %q", got)
				}
			})
		}
	}
}

func readCatalog(t *testing.T, path string) map[string]any {
	t.Helper()
	var out map[string]any
	if _, err := toml.DecodeFile(path, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func catalogForms(t *testing.T, key string, value any) map[string]string {
	t.Helper()
	switch value := value.(type) {
	case string:
		return map[string]string{"": value}
	case map[string]any:
		out := map[string]string{}
		for form, text := range value {
			if form != "zero" && form != "one" && form != "two" && form != "few" && form != "many" && form != "other" {
				t.Errorf("%s: unexpected plural form %s", key, form)
			}
			if s, ok := text.(string); ok {
				out[form] = s
			} else {
				t.Errorf("%s/%s: not text", key, form)
			}
		}
		return out
	default:
		t.Fatalf("%s: unexpected value %T", key, value)
		return nil
	}
}

var catalogFormat = regexp.MustCompile(`%(?:\[([1-9][0-9]*)\])?([dsv%])`)

func formatArguments(t *testing.T, key, text string) map[int]string {
	t.Helper()
	out, next := map[int]string{}, 1
	for _, match := range catalogFormat.FindAllStringSubmatch(text, -1) {
		if match[2] == "%" {
			continue
		}
		if match[1] != "" {
			next, _ = strconv.Atoi(match[1])
		}
		if previous, ok := out[next]; ok && previous != match[2] {
			t.Errorf("%s: argument %d has conflicting types", key, next)
		}
		out[next] = match[2]
		next++
	}
	if rest := catalogFormat.ReplaceAllString(text, ""); strings.Contains(rest, "%") {
		t.Errorf("%s: invalid or unsupported format in %q", key, text)
	}
	return out
}
