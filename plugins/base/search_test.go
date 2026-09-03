package alborzbase

import (
	"bufio"
	"strings"
	"testing"
)

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
