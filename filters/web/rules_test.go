package web_test

import (
	"maps"
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

// A Sieve server that takes no connection is a bad gateway, not an error of alborz's.
func TestAnUnreachableSieveServerIsABadGateway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := ln.Addr().String()
	ln.Close()
	a := Alborz(t, Upstream(Domain, "sieve+insecure://"+gone))
	c := a.SignIn(t, User)
	at := "\u2066" + User + "\u2069 (\u2066" + gone + "\u2069: " + T(t, "upstream.refused") + ")"
	for _, route := range []string{"filters.rules", "filters.forwarding", "filters.autoreply"} {
		resp := c.Get(t, route, url.Values{"account": {User}})
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("%s answered %s, want 502", route, resp.Status)
		}
		if !Says(t, resp.Text, "upstream.noanswer", T(t, "upstream.sieve"), at) {
			t.Errorf("%s does not say which server did not answer, where and why", route)
		}
	}
	// The list says it where the account's rows would be, and nothing of how alborz dialled.
	if list := c.Get(t, "filters.list"); !Says(t, list.Text, "upstream.noanswer", T(t, "upstream.sieve"), at) || strings.Contains(list.Text, "dial tcp") {
		t.Errorf("the list does not say which server did not answer, where and why: %q", AlertText(list.Text))
	}
	// Under All accounts each account's server is said, and the rail's warning names the filter server.
	c.SignIn(t, User2)
	merged := c.Get(t, "filters.list").Text
	why := "\u2069 (\u2066" + gone + "\u2069: " + T(t, "upstream.refused") + ")"
	if !Says(t, merged, "upstream.noanswer", T(t, "upstream.sieve"), "\u2066"+User2+why) || !Says(t, merged, "upstream.noanswer", T(t, "upstream.sieve"), at) {
		t.Errorf("the merged list does not say both accounts' servers did not answer")
	}
	if bar := NoticeBar(merged); !Says(t, bar, "notice.unreachable", 1, T(t, "servers.filters"), "\u2066"+Any+why) || !strings.Contains(bar, User2) {
		t.Errorf("the rail's warning does not name the filter server of %s: %s", User2, bar)
	}
	// A piece htmx asks for is answered in the plain text the notice shows whole.
	req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("filters.rules")+"?account="+url.QueryEscape(User), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	resp := c.Do(t, req)
	if want := T(t, "upstream.noanswer", T(t, "upstream.sieve"), at); resp.StatusCode != http.StatusBadGateway ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || strings.TrimSpace(resp.Text) != want {
		t.Errorf("the piece is %s %s %.200q, want 502 and %q", resp.Status, resp.Header.Get("Content-Type"), resp.Text, want)
	}
}

// A script deleted elsewhere is not found; a row named by no number is a bad request.
func TestAFilterOrRuleThatIsNotThere(t *testing.T) {
	c := Alborz(t, WithSieve(Sieve(t))).SignIn(t, User)
	account := url.Values{"account": {User}}
	if resp := c.Get(t, "filters.filter", "gone", account); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a missing script answered %s, want 404", resp.Status)
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
		maps.Copy(form, changed)
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
	if !strings.Contains(page, "Back on Monday") {
		t.Error("the text saved in the other browser is gone")
	}
	if !strings.Contains(page, `value="Out of office"`) {
		t.Error("the stale tab's subject is gone")
	}
}
