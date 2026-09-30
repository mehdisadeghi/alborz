package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// Blocking lands wherever the form says, not on a route, and leaves the rule in the script.
func TestBlockingASenderLandsOnTheMessage(t *testing.T) {
	sieve := Sieve(t)
	a := Alborz(t, WithSieve(sieve), Upstream("elsewhere.local", IMAP(t).Upstream()), Upstream("elsewhere.local", sieve.Upstream()))
	c := a.SignIn(t, User)
	inbox := a.Path("mail.mailbox", "INBOX")
	resp := c.Post(t, "filters.rules.block", url.Values{"value": {"spam@example.org"}, "next": {inbox}}, url.Values{"account": {User}})
	if at := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || at != inbox {
		t.Fatalf("block landed on %q with %s, want %s", at, resp.Status, inbox)
	}
	if !strings.Contains(c.Follow(t, inbox), "Mail from spam@example.org now goes to Junk.") {
		t.Errorf("the landing page does not say the sender was blocked")
	}
	if rules := sieve.Script("alborz-rules"); !strings.Contains(rules, `address :is "from" "spam@example.org"`) {
		t.Errorf("the rules script does not block the sender:\n%s", rules)
	}

	// Off-site is refused on either landing; the account elsewhere has no junk folder to block into.
	for account, c := range map[string]*Client{User: c, Elsewhere: a.SignIn(t, Elsewhere)} {
		for _, next := range []string{"https://evil.example/", "//evil.example/"} {
			resp := c.Post(t, "filters.rules.block", url.Values{"value": {"spam@example.org"}, "next": {next}}, url.Values{"account": {account}})
			if at := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasPrefix(at, a.Path("filters.rules")+"?") {
				t.Errorf("%s blocking with next %s: %s to %q, want the rules page", account, next, resp.Status, at)
			}
		}
	}
}
