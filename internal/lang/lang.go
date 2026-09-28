package lang

import (
	"strings"
	"unicode"
)

// Shorter text is no evidence, and a wrong language is worse than none.
const contentMinRunes = 10

// Persian is often typed with the Arabic yeh and kaf, so both forms are folded before counting.
var arabicFolding = strings.NewReplacer("ي", "ی", "ك", "ک")

// Letters Arabic does not have.
var persianLetters = []rune{'گ', 'چ', 'پ', 'ژ'}

var arabicLetters = []rune{'ة'}

// Words both languages use, and the name ali, are left out.
var arabicScriptWords = map[string][]string{
	"fa": {"است", "این", "که", "را", "برای", "به", "از", "در",
		"باید", "دارد", "شد", "می", "شما", "خود", "هست", "اینکه",
		"آن", "هم", "اگر", "نیز", "بود", "کرد", "شود", "ایم"},
	"ar": {"فی", "هذا", "هذه", "ذلک", "التی", "الذی", "إلی", "أن",
		"إن", "کان", "عن", "لیس", "قد", "لم", "لن", "وقد",
		"اللذین", "حتی", "أیضا", "ذلک"},
}

var latinMarks = map[string][]rune{
	"de": {'ä', 'ö', 'ü', 'ß'},
	"es": {'ñ', '¿', '¡'},
}

var latinWords = map[string][]string{
	"de": {"der", "die", "das", "und", "nicht", "für", "mit", "ist", "ein", "eine", "sich", "auch", "wird", "haben"},
	"es": {"el", "la", "los", "las", "que", "para", "con", "por", "una", "del", "es", "sus", "como"},
	"en": {"the", "and", "of", "to", "is", "for", "with", "you", "this", "that", "your", "from", "are"},
}

// OfContent names the language of interface text, "" where it is the page's own; never Arabic, as alborz ships no Arabic interface.
func OfContent(s, page string) string {
	return answer(classify(s, false), page)
}

// OfMessage is OfContent for a message's own text, where Arabic is a real possibility.
func OfMessage(s, page string) string {
	return answer(classify(s, true), page)
}

func answer(lang, page string) string {
	if lang == "" || lang == page {
		return ""
	}
	return lang
}

func classify(s string, mail bool) string {
	stripped := stripReplyPrefix(s)
	arab, latn := 0, 0
	for _, r := range stripped {
		switch {
		case unicode.Is(unicode.Arabic, r):
			arab++
		case unicode.Is(unicode.Latin, r):
			latn++
		}
	}
	if arab+latn < contentMinRunes {
		return ""
	}
	if arab > latn {
		if !mail {
			return "fa"
		}
		return arabicLang(stripped)
	}
	return latinLang(stripped)
}

// Persian stays the default: a Persian voice reading Arabic is a smaller error than the reverse.
func arabicLang(s string) string {
	folded := arabicFolding.Replace(s)

	words := map[string]int{}
	for _, word := range strings.FieldsFunc(folded, func(r rune) bool {
		return !unicode.IsLetter(r)
	}) {
		for lang, list := range arabicScriptWords {
			for _, w := range list {
				if word == w {
					words[lang]++
					break
				}
			}
		}
	}
	switch {
	case words["fa"] > words["ar"]:
		return "fa"
	case words["ar"] > words["fa"]:
		return "ar"
	}

	if containsAny(folded, persianLetters) {
		return "fa"
	}
	if containsAny(s, arabicLetters) {
		return "ar"
	}
	return "fa"
}

// Letters only settle a tie, so one borrowed diacritic cannot outvote English words.
func latinLang(s string) string {
	lower := strings.ToLower(s)

	words := map[string]int{}
	for _, word := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r)
	}) {
		for lang, list := range latinWords {
			for _, w := range list {
				if word == w {
					words[lang]++
					break
				}
			}
		}
	}
	if lang, ok := soleMax(words); ok {
		return lang
	}

	marks := map[string]int{}
	for lang, list := range latinMarks {
		for _, r := range list {
			marks[lang] += strings.Count(lower, string(r))
		}
	}
	if lang, ok := soleMax(marks); ok {
		return lang
	}
	return "en"
}

func soleMax(score map[string]int) (string, bool) {
	best, top, tied := "", 0, false
	for _, lang := range []string{"en", "de", "es"} {
		switch n := score[lang]; {
		case n > top:
			best, top, tied = lang, n, false
		case n == top && n > 0:
			tied = true
		}
	}
	return best, top > 0 && !tied
}

func containsAny(s string, runes []rune) bool {
	for _, r := range runes {
		if strings.ContainsRune(s, r) {
			return true
		}
	}
	return false
}

// "Re: " would otherwise count as Latin text.
func stripReplyPrefix(s string) string {
	for {
		trimmed := replyPrefix.ReplaceAllString(s, "")
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}
