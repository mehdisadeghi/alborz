package alborzbase

import (
	"bufio"
	"slices"
	"strings"
	"testing"
)

func TestQueryReadsPhrasesAndMinus(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    []queryTerm
		widened string
	}{
		{`"hello world"`, []queryTerm{{value: "hello world", quoted: true}}, `text:"hello world"`},
		{`offer -invoice`, []queryTerm{{value: "offer"}, {value: "invoice", not: true}}, `text:offer -text:invoice`},
		{`-"re: plan" from:eve`, []queryTerm{{value: "re: plan", not: true, quoted: true}, {key: "from", value: "eve"}}, `-text:"re: plan" from:eve`},
		{`hello world`, []queryTerm{{value: "hello world"}}, `text:"hello world"`},
	} {
		q := ParseQuery(tc.in)
		if !slices.Equal(q.terms, tc.want) {
			t.Errorf("%q: got %+v, want %+v", tc.in, q.terms, tc.want)
		}
		widened := q.Widened()
		if widened != tc.widened {
			t.Errorf("%q widened: got %q, want %q", tc.in, widened, tc.widened)
		}
		if again := ParseQuery(widened).Widened(); again != "" {
			t.Errorf("%q widened reads back with bare terms left: %q", tc.in, again)
		}
	}
	if folder, _ := ParseQuery("report -in:trash").Scope(); folder != "" {
		t.Errorf("-in:trash scopes the search to %q", folder)
	}
}

func TestSplitSearchTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`hello world foo:bar baz trains:"are cool"`, []string{"hello world", "foo:bar", "baz", "trains:are cool"}},
		// The shape that once spun forever inside the IMAP lock: a
		// single unquoted term with nothing after it.
		{"from:a@b.com", []string{"from:a@b.com"}},
		{`subject:"left open`, []string{"subject:left open"}},
		{"  spaced   out  ", []string{"spaced   out"}},
		{"", nil},
	} {
		sc := bufio.NewScanner(strings.NewReader(tc.in))
		sc.Split(splitSearchTokens)
		var got []string
		for sc.Scan() {
			got = append(got, sc.Text())
			if len(got) > len(tc.want)+1 {
				t.Fatalf("%q: the scanner does not advance: %v", tc.in, got)
			}
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}
