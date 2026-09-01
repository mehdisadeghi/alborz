package alborz

import (
	"strings"
	"unicode"
)

// The language a piece of mail is written in, which is not the same
// question as which way it runs. dir tells a browser how to lay text
// out; lang tells a screen reader which voice to read it in. Alborz
// answered only the first, so on a Persian page every English subject
// went to a Persian synthesiser, which reads Latin letters as noise.
//
// Script alone cannot answer it - Latin covers three of the four
// languages here - but the set is closed, so the choice is among four
// rather than among all, and that is small enough to decide by
// counting.

// contentMinRunes is how much text has to be there before the count is
// believed. A three-word subject carries no evidence, and claiming the
// wrong language is worse than leaving the page's own.
const contentMinRunes = 10

// Persian is often typed, or normalised on the way through some other
// system, with the Arabic yeh and kaf rather than the Persian ones. The
// same sentence then looks Arabic to anything counting codepoints, so
// the two forms are folded together before any of it is read.
var arabicFolding = strings.NewReplacer("ي", "ی", "ك", "ک")

// Four letters Arabic does not have at all, which no folding affects.
var persianLetters = []rune{'گ', 'چ', 'پ', 'ژ'}

// Teh marbuta ends an Arabic word and never a Persian one.
var arabicLetters = []rune{'ة'}

// What each language says constantly and the other does not, written in
// the folded form. Words the two share - man, ma, bad - are left out
// rather than guessed at, and so is ali, which is Arabic's "on" and one
// of the commonest Persian names.
var arabicScriptWords = map[string][]string{
	"fa": {"است", "این", "که", "را", "برای", "به", "از", "در",
		"باید", "دارد", "شد", "می", "شما", "خود", "هست", "اینکه",
		"آن", "هم", "اگر", "نیز", "بود", "کرد", "شود", "ایم"},
	"ar": {"فی", "هذا", "هذه", "ذلک", "التی", "الذی", "إلی", "أن",
		"إن", "کان", "عن", "لیس", "قد", "لم", "لن", "وقد",
		"اللذین", "حتی", "أیضا", "ذلک"},
}

// The words each language repeats often enough to be counted, and the
// letters only one of them spells with. Latin is the ambiguous script
// here: German and Spanish both mark themselves, and English is what is
// left when neither does.
var latinMarks = map[string][]rune{
	"de": {'ä', 'ö', 'ü', 'ß'},
	"es": {'ñ', '¿', '¡'},
}

var latinWords = map[string][]string{
	"de": {"der", "die", "das", "und", "nicht", "für", "mit", "ist", "ein", "eine", "sich", "auch", "wird", "haben"},
	"es": {"el", "la", "los", "las", "que", "para", "con", "por", "una", "del", "es", "sus", "como"},
	"en": {"the", "and", "of", "to", "is", "for", "with", "you", "this", "that", "your", "from", "are"},
}

// ContentLang names the language of text the interface shows - a name, a
// label, a folder, a filename - or returns "" where that is the page's
// own language and the attribute would say nothing.
//
// Arabic is never one of the answers here. Persian is full of Arabic
// words and they are Persian words now; alborz ships no Arabic
// interface, so on a Persian page there is nothing for an Arabic tag to
// be right about, and calling a Persian label Arabic hands it to the
// wrong voice for no gain.
func ContentLang(s, page string) string {
	return answer(classify(s, false), page)
}

// MessageLang is ContentLang for what a message itself says, where
// Arabic is a real possibility rather than Persian wearing loanwords.
// Mail is the one place the distinction can pay for itself.
func MessageLang(s, page string) string {
	return answer(classify(s, true), page)
}

func answer(lang, page string) string {
	if lang == "" || lang == page {
		return ""
	}
	return lang
}

// classify answers with a language tag or "" when the text does not say.
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

// arabicLang separates Persian from Arabic, words first and letters only
// to settle it. Counting codepoints alone called Persian typed with the
// Arabic yeh and kaf Arabic, which is how a great deal of Persian mail
// arrives. Persian stays the default: this is a Persian mailbox, and a
// Persian voice reading Arabic is a far smaller error than the reverse.
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

// latinLang votes among the three Latin languages alborz speaks. Words
// are asked first and letters only settle a tie: English borrows a
// diacritic often enough - "Your Über account statement" - that a
// single ü must not outvote the English words around it. A letter
// still decides where no word is recognised, which is the ordinary case
// for a short German or Spanish subject.
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

// soleMax names the one language with the highest score, or reports
// that nothing scored or two tied.
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

// stripReplyPrefix drops the marks a client puts in front of a subject,
// for the same reason SubjectDir does: "Re: " is two Latin letters in
// front of whatever the subject actually says.
func stripReplyPrefix(s string) string {
	for {
		trimmed := replyPrefix.ReplaceAllString(s, "")
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}
