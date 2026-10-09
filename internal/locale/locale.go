// Package locale holds the interface's strings in every language and writes numbers as each one does.
package locale

import (
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"

	"git.mehdix.org/alborz/locales"
)

var localizers = map[string]*i18n.Localizer{}
var bundle = i18n.NewBundle(language.English)

func init() {
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	if err := RegisterFS(locales.FS); err != nil {
		panic(fmt.Sprintf("alborz: cannot load core locales: %v", err))
	}
}

// RegisterFS adds one embedded translation file per supported language.
// Call it while assembling the app, before serving requests.
func RegisterFS(fsys fs.FS) error {
	for _, code := range Languages {
		name := code + ".toml"
		if _, err := bundle.LoadMessageFileFS(fsys, name); err != nil {
			return fmt.Errorf("load %s: %w", name, err)
		}
		localizers[code] = i18n.NewLocalizer(bundle, code, "en")
	}
	return nil
}

// Languages are the supported UI Languages, in fallback order.
var Languages = []string{"en", "fa", "de", "es"}

var languageTags = func() []language.Tag {
	tags := make([]language.Tag, len(Languages))
	for i, code := range Languages {
		tags[i] = language.MustParse(code)
	}
	return tags
}()

var languageMatcher = language.NewMatcher(languageTags)

// Match picks the language an Accept-Language header asks for,
// weighing q values, falling back to English.
func Match(header string) string {
	wanted, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return "en"
	}
	_, index, confidence := languageMatcher.Match(wanted...)
	if confidence == language.No {
		return "en"
	}
	return Languages[index]
}

// Supported reports whether the code names a supported UI language.
func Supported(code string) bool {
	return slices.Contains(Languages, code)
}

// T translates a key.
func T(lang, key string) string {
	return Plural(lang, key, 1)
}

// Plural translates a key in the plural form count calls for.
func Plural(lang, key string, count int) string {
	loc, ok := localizers[lang]
	if !ok {
		loc = localizers["en"]
	}
	// A message without plural forms is missed when asked with a count.
	if msg, err := loc.Localize(&i18n.LocalizeConfig{MessageID: key, PluralCount: count}); err == nil {
		return msg
	}
	if msg, err := loc.Localize(&i18n.LocalizeConfig{MessageID: key}); err == nil {
		return msg
	}
	return key
}

// Tf translates a format string and fills it; a first int argument is the count its plural form follows.
func Tf(lang, key string, args ...interface{}) string {
	count := 1
	if len(args) > 0 {
		if n, ok := args[0].(int); ok {
			count = n
		}
	}
	return Sprintf(lang, Plural(lang, key, count), args...)
}

// Choice is one interface language, named in itself.
type Choice struct {
	Code string
	Name string
}

var names = map[string]string{
	"en": "English",
	"fa": "فارسی",
	"de": "Deutsch",
	"es": "Español",
}

// Name is the language in its own name, empty for none supported.
func Name(code string) string { return names[code] }

// Choices lists every language.
func Choices() []Choice {
	out := make([]Choice, 0, len(Languages)+1)
	for _, code := range Languages {
		out = append(out, Choice{Code: code, Name: names[code]})
	}
	return out
}

var printers = map[string]*message.Printer{}

func init() {
	for _, code := range Languages {
		printers[code] = message.NewPrinter(language.MustParse(code))
	}
}

func printer(lang string) *message.Printer {
	if p, ok := printers[lang]; ok {
		return p
	}
	return printers["en"]
}

// Sprintf fills format as the language writes numbers.
func Sprintf(lang, format string, args ...any) string {
	return printer(lang).Sprintf(format, args...)
}

// Num writes a grouped count as the language writes one.
func Num(lang string, n int) string {
	return printer(lang).Sprintf("%d", n)
}

// Year writes a year in the language's digits, never grouped.
func Year(lang string, n int) string {
	return printer(lang).Sprint(number.Decimal(n, number.NoSeparator()))
}

var persianDigits = [10]rune{'۰', '۱', '۲', '۳', '۴', '۵', '۶', '۷', '۸', '۹'}

// Latin rewrites Persian and Arabic digits into Latin ones for parsing.
func Latin(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

// ReadInt reads a typed whole number in either digit shape, with the language's thousands separator.
func ReadInt(s string) (int, error) {
	return strconv.Atoi(strings.NewReplacer("٬", "", ",", "").Replace(Latin(strings.TrimSpace(s))))
}

// Shape writes s in the language's digits. A string still carrying a Latin word is left alone:
// half-converted reads worse than either script.
func Shape(lang, s string) string {
	if lang != "fa" || strings.ContainsFunc(s, isLatinLetter) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return persianDigits[r-'0']
		}
		return r
	}, s)
}

func isLatinLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
