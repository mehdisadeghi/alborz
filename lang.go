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

// Persian and Arabic share a script and are told apart by the letters
// each keyboard actually produces: Persian writes ی and ک where Arabic
// writes ي and ك, and has four letters Arabic does not.
var persianLetters = []rune{'ی', 'ک', 'گ', 'چ', 'پ', 'ژ'}

// Arabic's own marks: teh marbuta and the hamza carriers, none of which
// belong to ordinary Persian orthography.
var arabicLetters = []rune{'ة', 'أ', 'إ', 'ؤ', 'ئ', 'ي', 'ك'}

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

// ContentLang names the language a run of text should be read in, or
// returns "" when the answer is the language the page is already in and
// the attribute would say nothing. page is the UI language.
func ContentLang(s, page string) string {
	lang := classify(s)
	if lang == "" || lang == page {
		return ""
	}
	return lang
}

// classify answers with a language tag or "" when the text does not say.
func classify(s string) string {
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
		return arabicLang(stripped)
	}
	return latinLang(stripped)
}

// arabicLang separates Persian from Arabic. Persian is the default and
// Arabic needs saying so: this is a Persian mailbox, where mail in the
// script is Persian unless it marks itself otherwise, and a Persian
// voice reading Arabic is a far smaller error than the reverse.
func arabicLang(s string) string {
	if containsAny(s, persianLetters) {
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
