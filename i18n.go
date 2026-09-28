package alborz

import (
	"embed"
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml
var localeFS embed.FS

var localizers = map[string]*i18n.Localizer{}

func init() {
	bundle := i18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, code := range languages {
		name := "locales/" + code + ".toml"
		if _, err := bundle.LoadMessageFileFS(localeFS, name); err != nil {
			panic(fmt.Sprintf("alborz: cannot load %s: %v", name, err))
		}
		localizers[code] = i18n.NewLocalizer(bundle, code, "en")
	}
}

// langCookieName holds the language in the browser, since it is per person, not per account.
const langCookieName = "alborz_lang"

// languages are the supported UI languages, in fallback order.
var languages = []string{"en", "fa", "de", "es"}

var languageTags = func() []language.Tag {
	tags := make([]language.Tag, len(languages))
	for i, code := range languages {
		tags[i] = language.MustParse(code)
	}
	return tags
}()

var languageMatcher = language.NewMatcher(languageTags)

// MatchLanguage picks the language an Accept-Language header asks for,
// weighing q values, falling back to English.
func MatchLanguage(header string) string {
	wanted, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return "en"
	}
	_, index, confidence := languageMatcher.Match(wanted...)
	if confidence == language.No {
		return "en"
	}
	return languages[index]
}

// IsLanguage reports whether the code names a supported UI language.
func IsLanguage(code string) bool {
	return slices.Contains(languages, code)
}

func translate(lang, key string) string {
	return translateCount(lang, key, 1)
}

func translateCount(lang, key string, count int) string {
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

// LanguageChoice is one interface language, named in itself.
type LanguageChoice struct {
	Code string
	Name string
}

var languageNames = map[string]string{
	"en": "English",
	"fa": "فارسی",
	"de": "Deutsch",
	"es": "Español",
}

// LanguageChoices lists following the browser first, then every language.
func LanguageChoices() []LanguageChoice {
	out := make([]LanguageChoice, 0, len(languages)+1)
	for _, code := range languages {
		out = append(out, LanguageChoice{Code: code, Name: languageNames[code]})
	}
	return out
}
