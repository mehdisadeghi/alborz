package web_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func chooseDefaultAccount(t *testing.T, c *Client, account string) {
	t.Helper()
	if r := c.Post(t, "settings.profile", url.Values{"default_account": {account}}); r.StatusCode != http.StatusFound {
		t.Fatalf("choosing the default account %q: %s", account, r.Status)
	}
}

func TestNewMailUnderAllAccountsUsesTheDefaultAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	for account, text := range map[string]string{User: "First account signature", User2: "Default account signature"} {
		q := url.Values{"account": {account}}
		for _, setting := range []struct {
			route  string
			fields url.Values
		}{
			{"mail.signatures", url.Values{"name": {"Work"}, "text": {text}}},
			{"mail.signatures.default", url.Values{"name": {"Work"}}},
		} {
			if r := c.Post(t, setting.route, setting.fields, q); r.StatusCode != http.StatusFound {
				t.Fatalf("%s for %s: %s", setting.route, account, r.Status)
			}
		}
	}
	chooseDefaultAccount(t, c, User2)
	signed := func(page, what string) {
		t.Helper()
		if got := SelectedValue(page, "from_account"); got != User2 {
			t.Errorf("%s: From = %q, want %q", what, got, User2)
		}
		if SelectedValue(page, "signature") == "" || !strings.Contains(page, "Default account signature") || strings.Contains(page, "First account signature") {
			t.Errorf("%s: the message is not signed as the default account", what)
		}
	}

	// The URL names the account, so the draft, the signatures and the rail are its own.
	opened := c.Get(t, "mail.compose")
	if want := a.Path("mail.compose", url.Values{"account": {User2}}); opened.StatusCode != http.StatusFound || opened.Header.Get("Location") != want {
		t.Fatalf("compose under All accounts answered %s to %q, want %q", opened.Status, opened.Header.Get("Location"), want)
	}
	page := c.Follow(t, opened.Header.Get("Location"))
	signed(page, "new")
	if !strings.Contains(page, a.Path("mail.signatures", url.Values{"account": {User2}})) {
		t.Error("the signatures link is not the default account's")
	}
	if r := c.Post(t, "settings.profile", url.Values{"pool_signatures": {"1"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	signed(c.Page(t, "mail.compose"), "pooled signatures")

	if page := c.Page(t, "mail.compose", url.Values{"mailto": {"mailto:friend@example.org?subject=Hello"}}); !strings.Contains(page, `value="Hello"`) {
		t.Error("a mailto link lost its subject")
	} else {
		signed(page, "mailto")
	}
	shared := c.Upload(t, "mail.share", url.Values{"title": {"Trip notes"}}, nil)
	if page := c.Follow(t, shared.Header.Get("Location")); !strings.Contains(page, `value="Trip notes"`) {
		t.Error("a share lost its title")
	} else {
		signed(page, "share")
	}

	// A browser signed in only to the other account can still compose.
	if got := SelectedValue(a.SignIn(t, User).Page(t, "mail.compose"), "from_account"); got != User {
		t.Errorf("a default not signed in chose %q, want %q", got, User)
	}
}

// The URL's account outranks the stored default (ADR 1).
func TestDefaultAccountYieldsToTheAccountNamed(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	uid := a.IMAP.Append(t, "INBOX", fmt.Sprintf("From: list@lists.example.org\r\nTo: %s\r\nSubject: digest\r\n"+
		"Message-ID: <digest@test>\r\nList-Id: digest <digest.lists.example.org>\r\n"+
		"List-Unsubscribe: <mailto:leave@lists.example.org?subject=unsubscribe>\r\n"+
		"List-Help: <mailto:help@lists.example.org>\r\n"+
		"Content-Type: text/plain\r\n\r\nBody.\r\n", User))
	chooseDefaultAccount(t, c, User2)
	q := url.Values{"account": {User}}
	if got := SelectedValue(c.Page(t, "mail.compose", q), "from_account"); got != User {
		t.Errorf("compose for %s: From = %q", User, got)
	}

	compose := regexp.MustCompile(`href="(` + regexp.QuoteMeta(a.Path("mail.compose")) + `\?[^"]*)"`)
	links := map[string]string{}
	for _, m := range compose.FindAllStringSubmatch(c.Page(t, "mail.mailbox", "INBOX", q), -1) {
		links["New"] = html.UnescapeString(m[1])
	}
	for _, m := range compose.FindAllStringSubmatch(c.Page(t, "mail.message", "INBOX", uid, q), -1) {
		for _, list := range []string{"leave@lists.example.org", "help@lists.example.org"} {
			if href := html.UnescapeString(m[1]); strings.Contains(href, list) {
				links[list] = href
			}
		}
	}
	if len(links) != 3 {
		t.Fatalf("found the links %v, want New, unsubscribe and help", links)
	}
	for name, href := range links {
		if got := SelectedValue(c.Follow(t, href), "from_account"); got != User {
			t.Errorf("%s (%s): From = %q, want %q", name, href, got, User)
		}
	}
}

func TestDefaultAccountKeepsAnExplicitSender(t *testing.T) {
	smtp := SMTP(t)
	a := Alborz(t, WithSMTP(smtp))
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	page := c.Page(t, "mail.compose")
	chooseDefaultAccount(t, c, User2)
	// A tab opened before the default changed still sends as its selected account.
	c.Send(t, page, url.Values{"from_account": {User}}, "mail.compose")
	if !strings.Contains(smtp.Last(), "From: <"+User+">\r\n") {
		t.Fatalf("the default replaced the explicit sender: %s", headOf(smtp.Last()))
	}
	page = c.Page(t, "mail.compose")
	c.Send(t, page, url.Values{"from_account": {SelectedValue(page, "from_account")}}, "mail.compose", url.Values{"account": {User2}})
	if !strings.Contains(smtp.Last(), "From: <"+User2+">\r\n") {
		t.Fatalf("the default sender was not used for delivery: %s", headOf(smtp.Last()))
	}
}

func TestDefaultAccountLeavesRepliesAndDraftsAlone(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	q := url.Values{"account": {User}}
	// Store a draft with an explicit sender before changing the default.
	r := c.Compose(t, c.Page(t, "mail.compose", q), url.Values{"save_as_draft": {"1"}, "from_account": {User}}, "mail.compose", q)
	if r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	draft := r.Header.Get("Location")
	chooseDefaultAccount(t, c, User2)
	if got := SelectedValue(c.Follow(t, draft), "from_account"); got != User {
		t.Errorf("draft sender = %q, want %q", got, User)
	}
	uids := MessageUIDs(c.Page(t, "mail.mailbox", "INBOX", q))
	if len(uids) == 0 {
		t.Fatal("no seeded messages")
	}
	for _, route := range []string{"mail.message.reply", "mail.message.forward"} {
		if got := SelectedValue(c.Page(t, route, "INBOX", uids[0], q), "from_account"); got != User {
			t.Errorf("%s sender = %q, want %q", route, got, User)
		}
	}
}

// A new message under All accounts is the default account's whole: its draft is kept there and leaves from there.
func TestDefaultAccountKeepsItsOwnDrafts(t *testing.T) {
	for _, sender := range []string{User, User2} {
		t.Run(sender, func(t *testing.T) {
			smtp := SMTP(t)
			a := Alborz(t, WithSMTP(smtp))
			c := a.SignIn(t, User)
			c.SignIn(t, User2)
			chooseDefaultAccount(t, c, sender)
			q := url.Values{"account": {sender}}
			before := MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", q))
			page := c.Page(t, "mail.compose")
			r := c.Compose(t, page, url.Values{"save_as_draft": {"1"}, "from_account": {SelectedValue(page, "from_account")}}, "mail.compose", q)
			if r.StatusCode != http.StatusFound {
				t.Fatal(r.Status)
			}
			page = c.Follow(t, r.Header.Get("Location"))
			if got := SelectedValue(page, "from_account"); got != sender {
				t.Fatalf("the draft reopened with From %q, want %q", got, sender)
			}
			drafts := MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", q))
			if len(drafts) != len(before)+1 {
				t.Fatalf("%s keeps %d drafts, want %d", sender, len(drafts), len(before)+1)
			}
			added := slices.DeleteFunc(drafts, func(uid string) bool { return slices.Contains(before, uid) })
			c.Send(t, page, url.Values{"from_account": {sender}}, "mail.message.edit", "Drafts", added[0], q)
			if !strings.Contains(smtp.Last(), "From: <"+sender+">") {
				t.Fatalf("the draft left with the wrong From: %s", headOf(smtp.Last()))
			}
			if n := len(MessageUIDs(c.Page(t, "mail.mailbox", "Drafts", q))); n != len(before) {
				t.Errorf("the sent draft is still on %s", sender)
			}
		})
	}
}
