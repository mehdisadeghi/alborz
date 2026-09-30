package search

import (
	"bufio"
	"slices"
	"strings"
	"testing"
)

func TestQueryReadsPhrasesAndMinus(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Query
	}{
		{`"hello world"`, Query{{Value: "hello world", Quoted: true}}},
		{`offer -invoice`, Query{{Value: "offer"}, {Value: "invoice", Not: true}}},
		{`-"re: plan" from:eve`, Query{{Value: "re: plan", Not: true, Quoted: true}, {Key: "from", Value: "eve"}}},
		{`hello world`, Query{{Value: "hello world"}}},
		{`-Nobody:knows`, Query{{Key: "nobody", Value: "knows", Not: true}}},
		{`after: :x`, Query{{Value: "after:"}, {Value: ":x"}}},
	} {
		if got := Parse(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("%q: got %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestQueryKeysAndWords(t *testing.T) {
	q := Parse(`report from:eve -from:bob "a plan" is:starred`)
	if got, want := q.Keys(), []string{"from", "is"}; !slices.Equal(got, want) {
		t.Errorf("keys: got %q, want %q", got, want)
	}
	if got, want := q.Words(), (Query{{Value: "report"}, {Value: "a plan", Quoted: true}}); !slices.Equal(got, want) {
		t.Errorf("words: got %+v, want %+v", got, want)
	}
	if got, want := q.String(), `report from:eve -from:bob "a plan" is:starred`; got != want {
		t.Errorf("written back: got %q, want %q", got, want)
	}
	if got, want := q.Without("from"), `report "a plan" is:starred`; got != want {
		t.Errorf("without from: got %q, want %q", got, want)
	}
}

func TestSplitSearchTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`hello world foo:bar baz trains:"are cool"`, []string{"hello world", "foo:bar", "baz", "trains:are cool"}},
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
