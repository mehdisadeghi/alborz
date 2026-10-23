package mail

import (
	"testing"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/memo"
)

func TestAuthServSuggestionsNeedConfirmation(t *testing.T) {
	p := &Mailboxes{authServGuesses: memo.New[string](authSampleTTL)}
	p.authServGuesses.Put("", "mx.example.org")
	login := new(alborz.Login)
	if got := p.SuggestAuthServ(login); got != "mx.example.org" {
		t.Fatalf("suggestion = %q", got)
	}
	if got := p.TrustedAuthServ(login, &Settings{}); got != "" {
		t.Errorf("unconfirmed suggestion trusted: %q", got)
	}
	if got := p.TrustedAuthServ(login, &Settings{TrustedAuthServ: "mx.example.org"}); got != "mx.example.org" {
		t.Errorf("configured trust = %q", got)
	}
}

func TestAuthenticationResultsRequireTheExactLocalServer(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		trusted      bool
	}{
		{"local", "Authentication-Results: mx.example.org; dkim=pass header.d=example.org\n\n", true},
		{"sibling", "Authentication-Results: other.example.org; dkim=pass header.d=example.org\n\n", false},
		{"arc", "ARC-Authentication-Results: i=1; mx.example.org; dkim=pass header.d=example.org\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readAuthResults(headerOf(t, tc.header), "mx.example.org")
			if (got != nil) != tc.trusted {
				t.Errorf("trusted = %v, want %v", got, tc.trusted)
			}
		})
	}
}

func TestFlowedSignatureStartsItsOwnLine(t *testing.T) {
	for _, delsp := range []bool{false, true} {
		got := Unflow("paragraph \r\n-- \r\nsignature", delsp)
		want := "paragraph \n-- \nsignature"
		if delsp {
			want = "paragraph\n-- \nsignature"
		}
		if got != want {
			t.Errorf("delsp=%v: %q", delsp, got)
		}
	}
}

func TestListURIsKeepCommasAndIgnoreComments(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"<https://example.org/a,b>", "https://example.org/a,b"},
		{"(help, here) <https://example.org/help> (visit)", "https://example.org/help"},
		{"(ignore <https://evil.example/>) <https://example.org/help>", "https://example.org/help"},
		{"<mailto:list@example.org>, <https://example.org/help>", "https://example.org/help"},
		{"<ftp://example.org/a>, <mailto:list@example.org>", "mailto:list@example.org"},
	} {
		if got := listAction(tc.header); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestUnsubscribePostKeepsOrdinaryLink(t *testing.T) {
	for _, signal := range []string{"List-Unsubscribe=One-Click", "garbage one-click", "List-Unsubscribe=One-Click; extra=value", ""} {
		msg := new(IMAPMessage)
		msg.setListHeaders(headerOf(t, "List-Unsubscribe: <https://example.org/leave>\nList-Unsubscribe-Post: "+signal+"\nAuthentication-Results: mx.example.org; dkim=pass header.d=example.org\nDKIM-Signature: v=1; d=example.org; h=list-unsubscribe:list-unsubscribe-post; b=forged\n\n"))
		if msg.ListUnsubscribe != "https://example.org/leave" {
			t.Errorf("ordinary link lost: %q", msg.ListUnsubscribe)
		}
	}
}
