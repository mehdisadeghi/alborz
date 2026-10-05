package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A sign-in goes on to the page that asked for it, and never off the site.
func TestSignInStaysOnTheSite(t *testing.T) {
	a := Alborz(t)
	// The first sign-in also opens the tour (ADR 76), which is not what this is about.
	a.SignIn(t, User)
	inbox := a.Path("mail.mailbox", "INBOX")
	for next, want := range map[string]string{
		a.Path("settings.profile"): a.Path("settings.profile"),
		"//evil.example/":          inbox,
		"https://evil.example/":    inbox,
		`/\evil`:                   inbox,
		a.Path("auth.login"):       inbox,
	} {
		resp := a.Client().Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "next": {next}})
		if got := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || got != want {
			t.Errorf("next %q answered %s to %q, want %q", next, resp.Status, got, want)
		}
	}
}

func TestAccountsScopeAndSignOut(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)

	merged := c.Page(t, "mail.mailbox", "INBOX")
	for _, account := range []string{User, User2} {
		if !strings.Contains(merged, `href="`+a.Path("mail.mailbox", "INBOX", url.Values{"account": {account}})+`"`) {
			t.Errorf("the merged view's rail does not offer %s", account)
		}
	}
	if !strings.Contains(merged, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the merged view has no merged entry in the rail")
	}
	scoped := c.Page(t, "mail.mailbox", "INBOX", url.Values{"account": {User2}})
	if !strings.Contains(scoped, `popovertarget="menu-accounts"><span class="label" dir="ltr"><bdi class="ltr">`+User2) {
		t.Errorf("a page scoped to %s does not say so in the nav", User2)
	}
	if !strings.Contains(scoped, `class="acct-folders aggregate-folders"`) {
		t.Errorf("the scoped view lost the merged entry from the rail")
	}

	for _, account := range []string{User, User2} {
		if resp := c.Post(t, "auth.logout", url.Values{"account": {account}}); resp.StatusCode != http.StatusFound {
			t.Fatalf("logout %s: %s", account, resp.Status)
		}
	}
	resp := c.Post(t, "mail.mailbox", nil, "INBOX")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), a.Path("auth.login")) {
		t.Errorf("after signing out of every account a page still answered %s to %s", resp.Status, resp.Header.Get("Location"))
	}
}
