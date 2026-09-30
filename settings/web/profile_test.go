package web_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func perPage(t *testing.T, c *Client) string {
	t.Helper()
	m := regexp.MustCompile(`id="messages_per_page"\s+value="(\d+)"`).FindStringSubmatch(c.Page(t, "settings.profile"))
	if m == nil {
		t.Fatal("the settings page shows no page size")
	}
	return m[1]
}

func TestProfileReachesEveryAccount(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	for _, account := range []string{User, User2} {
		if got := perPage(t, a.SignIn(t, account)); got != "17" {
			t.Errorf("%s holds a page size of %s, want 17", account, got)
		}
	}
}

// A mail server with METADATA keeps the profile, so another alborz reading it opens with it.
func TestProfileTravelsBetweenAlborz(t *testing.T) {
	srv := IMAP(t, Metadata())
	first, second := Alborz(t, WithIMAP(srv)), Alborz(t, WithIMAP(srv))
	if resp := first.SignIn(t, User).Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	if got := perPage(t, second.SignIn(t, User)); got != "17" {
		t.Errorf("the other alborz holds a page size of %s, want 17", got)
	}
}

func TestSigningInAskedAboutAnotherProfile(t *testing.T) {
	for _, answer := range []struct{ route, want string }{
		{"settings.profile.take", "17"},
		{"settings.profile.discard", "50"},
	} {
		t.Run(answer.route, func(t *testing.T) {
			a := Alborz(t)
			a.SignIn(t, User).Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}})

			c := a.SignIn(t, User2)
			resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}}, url.Values{"add": {"1"}})
			bar := NoticeBar(c.Follow(t, resp.Header.Get("Location")))
			if !strings.Contains(bar, "Messages per page") || !strings.Contains(bar, "17") || !strings.Contains(bar, "50") {
				t.Fatalf("no notice names the differing page size:\n%s", bar)
			}
			if got := perPage(t, c); got != "50" {
				t.Fatalf("the session took %s before the reader answered", got)
			}

			var fields url.Values
			for _, f := range regexp.MustCompile(`(?s)<form method="post" action="([^"]+)" class="notice-action">(.*?)</form>`).FindAllStringSubmatch(bar, -1) {
				if f[1] != a.Path(answer.route) {
					continue
				}
				fields = url.Values{}
				for _, in := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(f[2], -1) {
					fields.Add(in[1], html.UnescapeString(in[2]))
				}
			}
			if fields == nil {
				t.Fatalf("the notice offers no %s:\n%s", answer.route, bar)
			}
			if resp := c.Post(t, answer.route, fields); resp.StatusCode != http.StatusFound {
				t.Fatalf("answering: %s", resp.Status)
			}

			if got := perPage(t, c); got != answer.want {
				t.Errorf("the session reads %s, want %s", got, answer.want)
			}
			for _, account := range []string{User, User2} {
				if got := perPage(t, a.SignIn(t, account)); got != answer.want {
					t.Errorf("%s holds %s, want %s", account, got, answer.want)
				}
			}
		})
	}
}

// Entries another program or version wrote in a shape alborz does not read are read as far as they go:
// every page opens, the reader is told once, and what did decode stays.
func TestAStoredEntryThatDoesNotDecodeIsReadAsFarAsItGoes(t *testing.T) {
	srv := IMAP(t, Metadata())
	for key, value := range map[string]string{
		"base.settings":        `{"From": "Ada Lovelace", "Signatures": "not a list"}`,
		"caldav.settings":      `not json`,
		"caldav.subscriptions": `[`,
		"carddav.settings":     `{"AddressBookFilter": "yes"}`,
	} {
		srv.SetEntry(User, key, value)
	}
	c := Alborz(t, WithIMAP(srv)).Client()
	signIn := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}})
	if signIn.StatusCode != http.StatusFound {
		t.Fatalf("signing in answered %s", signIn.Status)
	}
	const warning = "Some saved settings could not be read."
	warned := 0
	if strings.Contains(NoticeBar(c.Follow(t, signIn.Header.Get("Location"))), warning) {
		warned++
	}
	for _, route := range []string{"mail.account", "calendar.month", "calendar.tasks", "contacts.list", "mail.mailbox"} {
		args := []any{}
		if route == "mail.mailbox" {
			args = append(args, "INBOX")
		}
		resp := c.Get(t, route, args...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %s", route, resp.Status)
		}
		if strings.Contains(NoticeBar(resp.Text), warning) {
			warned++
		}
		if route == "mail.account" && !strings.Contains(resp.Text, `value="Ada Lovelace"`) {
			t.Error("the name that decoded is not shown")
		}
	}
	if warned != 1 {
		t.Errorf("the reader was warned %d times, want once", warned)
	}
}

// A limit lowered since an entry was written holds for what the reader saves, not for what is read.
func TestAStoredEntryPastALimitIsRead(t *testing.T) {
	srv := IMAP(t, Metadata())
	long := strings.Repeat("Ada ", 200)
	srv.SetEntry(User, "base.settings", `{"From": "`+long+`"}`)
	resp := Alborz(t, WithIMAP(srv)).SignIn(t, User).Get(t, "mail.account")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Text, long) {
		t.Errorf("the account page answered %s without the stored name", resp.Status)
	}
}
