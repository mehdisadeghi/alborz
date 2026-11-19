package imapsearch

import (
	"reflect"
	"testing"
	"time"

	"git.mehdix.org/alborz/search"
)

func TestQueryWidensBareTerms(t *testing.T) {
	for _, tc := range []struct{ in, widened string }{
		{`"hello world"`, `text:"hello world"`},
		{`offer -invoice`, `text:offer -text:invoice`},
		{`-"re: plan" from:eve`, `-text:"re: plan" from:eve`},
		{`hello world`, `text:"hello world"`},
		{`after:yesterday`, `text:after:yesterday`},
	} {
		widened := Widened(search.Parse(tc.in))
		if widened != tc.widened {
			t.Errorf("%q widened: got %q, want %q", tc.in, widened, tc.widened)
		}
		if again := Widened(search.Parse(widened)); again != "" {
			t.Errorf("%q widened reads back with bare terms left: %q", tc.in, again)
		}
	}
	if folder, _ := Scope(search.Parse("report -in:trash")); folder != "" {
		t.Errorf("-in:trash scopes the search to %q", folder)
	}
}

// What alborz reads of a query itself, its keys, states, units and scope, it reads in any case; the
// text is the server's to match.
func TestQueryReadsItsOwnWordsInAnyCase(t *testing.T) {
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	for _, pair := range [][2]string{
		{"FROM:eve", "from:eve"},
		{"Is:UNREAD", "is:unread"},
		{"is:Starred", "is:flagged"},
		{"newer_than:2D", "newer_than:2d"},
		{"Older_Than:3W", "older_than:3w"},
		{"newer_than:1M", "newer_than:1m"},
		{"older_than:1Y", "older_than:1y"},
		{"larger:5M", "larger:5m"},
		{"smaller:10K", "smaller:10k"},
	} {
		if !reflect.DeepEqual(Of(search.Parse(pair[0]), false, now), Of(search.Parse(pair[1]), false, now)) {
			t.Errorf("%q and %q ask the server for different things", pair[0], pair[1])
		}
	}
	for _, query := range []string{"in:ANYWHERE", "IN:All", "in:anywhere"} {
		if _, everywhere := Scope(search.Parse(query)); !everywhere {
			t.Errorf("%q does not search every folder", query)
		}
	}
}
