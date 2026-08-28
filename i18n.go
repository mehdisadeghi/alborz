package alborz

import (
	"embed"
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// The UI strings live in locales/, one file per language, so a
// translator never opens a Go file. go-i18n carries the plural rules
// CLDR defines for each language, which is why nothing here says
// "%d message(s)" any more.
//
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

// langCookieName stores the user's explicit language choice; language
// is per user, not per account, so it lives in the browser rather than
// any one account's settings store.
const langCookieName = "alborz_lang"

// languages are the supported UI languages, in fallback order.
var languages = []string{"en", "fa", "de", "es"}

// languageTags mirror languages, in the same order, for the matcher.
var languageTags = func() []language.Tag {
	tags := make([]language.Tag, len(languages))
	for i, code := range languages {
		tags[i] = language.MustParse(code)
	}
	return tags
}()

var languageMatcher = language.NewMatcher(languageTags)

// MatchLanguage picks the language an Accept-Language header asks for,
// falling back to English. x/text weighs the q values, which reading the
// header in order does not: "de;q=0.1, fa;q=0.9" asks for Persian.
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

// translate returns a key's message in a language, falling back to
// English and then to the key itself.
func translate(lang, key string) string {
	return translateCount(lang, key, 1)
}

// translateCount picks the plural form a count calls for in that
// language, which is not always the two English has.
func translateCount(lang, key string, count int) string {
	loc, ok := localizers[lang]
	if !ok {
		loc = localizers["en"]
	}
	// Asking for a plural form of a message that has only one misses
	// it, so a message without plurals is looked up without a count.
	if msg, err := loc.Localize(&i18n.LocalizeConfig{MessageID: key, PluralCount: count}); err == nil {
		return msg
	}
	if msg, err := loc.Localize(&i18n.LocalizeConfig{MessageID: key}); err == nil {
		return msg
	}
	return key
}
