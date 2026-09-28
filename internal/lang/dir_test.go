package lang

import (
	"strings"
	"testing"
)

func TestHTMLAlternativeDirectionPerParagraph(t *testing.T) {
	out := HTMLAlternative("سلام، این یک پیام فارسی است.\n\nAnd this paragraph is English.")
	rtl := strings.Index(out, `<p dir="rtl">`)
	ltr := strings.Index(out, `<p dir="ltr">`)
	if rtl < 0 || ltr < 0 {
		t.Fatalf("both directions must appear:\n%s", out)
	}
	if rtl > ltr {
		t.Errorf("paragraphs came out in the wrong order:\n%s", out)
	}
	if n := strings.Count(out, "<p "); n != 2 {
		t.Errorf("expected two paragraphs, got %d:\n%s", n, out)
	}
}

func TestHTMLAlternativeQuoteAsksItself(t *testing.T) {
	out := HTMLAlternative("My answer in English.\n\n> سلام، این نقل قول فارسی است.")
	if !strings.Contains(out, "<blockquote>") {
		t.Fatalf("quoted lines did not become a blockquote:\n%s", out)
	}
	quote := out[strings.Index(out, "<blockquote>"):]
	if !strings.Contains(quote, `<p dir="rtl">`) {
		t.Errorf("the quote was not read for its own direction:\n%s", out)
	}
	if strings.Contains(quote, "&gt;") {
		t.Errorf("the quote marks were carried into the text:\n%s", out)
	}
}

func TestHTMLAlternativeEscapesAndKeepsBreaks(t *testing.T) {
	out := HTMLAlternative("a <b>bold</b> claim & more\nsecond line")
	if strings.Contains(out, "<b>") {
		t.Errorf("markup in the text was not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;b&gt;bold&lt;/b&gt;") || !strings.Contains(out, "&amp;") {
		t.Errorf("the text did not survive escaping:\n%s", out)
	}
	if !strings.Contains(out, "<br>") {
		t.Errorf("a typed line break was lost:\n%s", out)
	}
	for _, banned := range []string{"style=", "<div", "<font", "class="} {
		if strings.Contains(out, banned) {
			t.Errorf("generated %q, which is more than direction:\n%s", banned, out)
		}
	}
}

func TestParagraphDirSurvivesLeadingBreak(t *testing.T) {
	for in, want := range map[string]string{
		"\nfoobar":   "ltr",
		"\r\nسلام":   "rtl",
		"\u2029text": "ltr",
	} {
		if got := ParagraphDir(in); got != want {
			t.Errorf("ParagraphDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasRTL(t *testing.T) {
	for text, want := range map[string]bool{
		"":                                false,
		"Only English here.\n\nAnd more.": false,
		"سلام\n\nEnglish.":                true,
		"My answer in English.\n\n> سلام، این": true,
	} {
		if got := HasRTL(text); got != want {
			t.Errorf("HasRTL(%q) = %v, want %v", text, got, want)
		}
	}
}
