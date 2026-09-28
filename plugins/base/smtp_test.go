package alborzbase

import (
	"strings"
	"testing"

	"github.com/emersion/go-message"
)

func TestWrapTextStuffsAndKeepsHardBreaksHard(t *testing.T) {
	long := strings.Repeat("word ", 14) + "From the start of the next line"
	text := " indented\nFrom here on\nends in a space \nnext line\n-- \nsig\n" + long
	want := "  indented\n From here on\nends in a space\nnext line\n-- \nsig\n" +
		strings.TrimSuffix(strings.Repeat("word ", 14), " ") + " \n" + " From the start of the next line"
	if got := wrapText(text); got != want {
		t.Errorf("wrapText:\n%q\nwant\n%q", got, want)
	}
}

func TestUnflowJoinsSoftBreaksAndUnstuffs(t *testing.T) {
	flowed := "Hello \r\nworld.\r\n From the top\r\n  indented\r\n>> quoted \r\n>> on\r\n> shallower\r\n-- \r\nsig"
	want := "Hello world.\nFrom the top\n indented\n>> quoted on\n> shallower\n-- \nsig"
	if got := Unflow(flowed, false); got != want {
		t.Errorf("Unflow:\n%q\nwant\n%q", got, want)
	}
	if got, want := Unflow("Hel \r\nlo", true), "Hello"; got != want {
		t.Errorf("Unflow with DelSp: %q, want %q", got, want)
	}
}

func TestUnflowReadsWhatWrapTextWrites(t *testing.T) {
	text := strings.Repeat("word ", 30) + "From end\n From stuffed"
	want := strings.Repeat("word ", 30) + "From end\n From stuffed"
	if got := Unflow(wrapText(text), false); got != want {
		t.Errorf("round trip:\n%q\nwant\n%q", got, want)
	}
}

func flowedPart(t *testing.T, params, body string) *message.Entity {
	t.Helper()
	var h message.Header
	h.Set("Content-Type", "text/plain; charset=utf-8; "+params)
	part, err := message.New(h, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return part
}

func TestQuoteJoinsFlowedParagraphs(t *testing.T) {
	for _, c := range []struct{ params, body string }{
		{"format=flowed", "The quick \r\nbrown fox.\r\nNext"},
		{"format=flowed; delsp=yes", "The quick  \r\nbrown fox.\r\nNext"},
	} {
		text, err := readText(flowedPart(t, c.params, c.body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := quote(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		if want := "> The quick brown fox.\n> Next\n\n"; got != want {
			t.Errorf("%s: quote:\n%q\nwant\n%q", c.params, got, want)
		}
	}
}

func TestQuoteNestsMarkersWithoutSpaces(t *testing.T) {
	got, err := quote(strings.NewReader("> earlier\nreply"))
	if err != nil {
		t.Fatal(err)
	}
	if want := ">> earlier\n> reply\n\n"; got != want {
		t.Errorf("quote:\n%q\nwant\n%q", got, want)
	}
}

func TestQuoteOfQuoteSurvivesUnflow(t *testing.T) {
	long := strings.TrimSuffix(strings.Repeat("word ", 30), " ")
	quoted, err := quote(strings.NewReader("> " + long))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := wrapText(quoted)
	for _, line := range strings.Split(strings.TrimSpace(wrapped), "\n") {
		if line = strings.TrimSuffix(line, " "); len(line) > wrapColumn {
			t.Errorf("line of %d exceeds %d: %q", len(line), wrapColumn, line)
		}
	}
	if got, want := Unflow(wrapped, false), ">> "+long+"\n\n"; got != want {
		t.Errorf("round trip:\n%q\nwant\n%q", got, want)
	}
}
