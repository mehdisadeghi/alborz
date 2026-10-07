package web_test

import (
	"net"
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
	if !Says(t, c.Follow(t, inbox), "notice.blocked", "spam@example.org") {
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

// A Sieve server that takes no connection gets the page that says so, not an error of alborz's.
func TestAnUnreachableSieveServerIsNamed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := ln.Addr().String()
	ln.Close()
	c := Alborz(t, Upstream(Domain, "sieve+insecure://"+gone)).SignIn(t, User)
	for _, route := range []string{"filters.rules", "filters.forwarding", "filters.autoreply"} {
		resp := c.Get(t, route, url.Values{"account": {User}})
		server := T(t, "upstream.sieve")
		if resp.StatusCode != http.StatusBadGateway || !Says(t, resp.Text, "upstream.unreachable", server) || !Says(t, Between(resp.Text, "<h1>", "</h1>"), "upstream.title", server) {
			t.Errorf("%s answered %s without naming the server", route, resp.Status)
		}
	}
}

// A script deleted elsewhere is not found; a row named by no number is a bad request.
func TestAFilterOrRuleThatIsNotThere(t *testing.T) {
	c := Alborz(t, WithSieve(Sieve(t))).SignIn(t, User)
	account := url.Values{"account": {User}}
	if resp := c.Get(t, "filters.filter", "gone", account); resp.StatusCode != http.StatusNotFound || !Says(t, resp.Text, "notfound.script", "gone") {
		t.Errorf("a missing script answered %s without saying so", resp.Status)
	}
	for _, route := range []string{"filters.rules", "filters.rules.delete", "filters.rules.move", "filters.rules.toggle", "filters.autoreply", "filters.autoreply.delete"} {
		if resp := c.Post(t, route, url.Values{"index": {"first"}, "name": {"Rule"}, "text": {"Away"}}, account); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with index first answered %s, want 400", route, resp.Status)
		}
	}
}

// An auto-reply edited in a tab drawn before another browser saved it keeps the other browser's change.
func TestAStaleAutoReplyEditKeepsAnotherBrowsersSave(t *testing.T) {
	a := Alborz(t, WithSieve(Sieve(t)))
	stale, other := a.SignIn(t, User), a.SignIn(t, User)
	account := url.Values{"account": {User}}
	reply := func(c *Client, shown string, changed url.Values) *Response {
		form := url.Values{"shown": {shown}, "index": {"0"}, "name": {"Away"}, "subject": {""}, "text": {"Back soon"}, "days": {"7"}, "addresses": {User}}
		for k, v := range changed {
			form[k] = v
		}
		return c.Post(t, "filters.autoreply", form, account)
	}
	if resp := reply(other, "", url.Values{"index": nil}); resp.StatusCode != http.StatusFound {
		t.Fatalf("adding the reply answered %s", resp.Status)
	}
	drawn := Shown(stale.Page(t, "filters.autoreply.reply", 0, account))
	if resp := reply(other, Shown(other.Page(t, "filters.autoreply.reply", 0, account)), url.Values{"text": {"Back on Monday"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("changing the text answered %s", resp.Status)
	}
	if resp := reply(stale, drawn, url.Values{"subject": {"Out of office"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("changing the subject answered %s", resp.Status)
	}
	page := other.Page(t, "filters.autoreply.reply", 0, account)
	if !strings.Contains(page, `name="subject"`) {
		t.Fatal("the reply page shows no form")
	}
	if !strings.Contains(page, "Back on Monday") {
		t.Error("the text saved in the other browser is gone")
	}
	if !strings.Contains(page, `value="Out of office"`) {
		t.Error("the stale tab's subject is gone")
	}
}
