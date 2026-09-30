package imapsearch

import (
	"testing"

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
