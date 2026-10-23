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
	if !strings.Contains(scoped, `<span class="label" dir="ltr"><bdi class="ltr">`+User2) {
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

func TestGetLoginCannotAddAnAccount(t *testing.T) {
	a := Alborz(t)
	c := a.Client()
	resp := c.Get(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET login answered %s", resp.Status)
	}
	if got := len(a.Server.Logins()); got != 0 {
		t.Errorf("GET authenticated %d accounts", got)
	}
	c.SignIn(t, User)
	c.Get(t, "auth.login", url.Values{"add": {"1"}, "username": {User2}, "password": {Password}})
	if got := len(a.Server.Logins()); got != 1 {
		t.Errorf("GET added an account: %d logins", got)
	}
}

func TestRemovingLastRememberedAccountDeletesPersistedCredentials(t *testing.T) {
	dir := t.TempDir()
	a := Alborz(t, DataDir(dir))
	c := a.Client()
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatal(resp.Status)
	}
	c.SignIn(t, User2)
	var id string
	signed, err := a.Server.Sessions.SignedIn(User, "")
	if err != nil || len(signed) != 1 {
		t.Fatalf("sessions=%v err=%v", signed, err)
	}
	id = signed[0].ID
	if err := a.Server.Sessions.SignOut(id, User); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Server.SessionRecords.Load(id); ok {
		t.Error("removed credentials remain on disk and can be restored after restart")
	}
	if !a.Server.Sessions.Holds(id, User2) {
		t.Error("unremembered account was signed out")
	}
	a.Close()
	restarted := Alborz(t, DataDir(dir), WithIMAP(a.IMAP))
	resumed := restarted.Client()
	resumed.HTTP.Jar = c.HTTP.Jar
	if resp := resumed.Get(t, "auth.login"); resp.StatusCode != http.StatusOK {
		t.Errorf("restart restored removed account: %s", resp.Status)
	}
	if got := len(restarted.Server.Logins()); got != 0 {
		t.Errorf("restart authenticated %d removed accounts", got)
	}
}
