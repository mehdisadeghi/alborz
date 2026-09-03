package alborz

import (
	"strings"
	"testing"
)

// The whole point: a Persian paragraph has to carry dir="rtl" and an
// English one dir="ltr", in the same message, because the recipient's
// client works neither out for itself.
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

// A quote often runs the other way from the reply around it, so it is
// asked separately rather than inheriting.
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

// It is an alternative to the text part, not a rewrite of it: the words
// have to survive intact, and anything that looks like markup in them
// must not become markup.
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
	// No styling of any kind: this must not read as "HTML mail".
	for _, banned := range []string{"style=", "<div", "<font", "class="} {
		if strings.Contains(out, banned) {
			t.Errorf("generated %q, which is more than direction:\n%s", banned, out)
		}
	}
}
