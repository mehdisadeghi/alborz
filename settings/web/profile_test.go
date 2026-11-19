package web_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
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

func TestDefaultOutgoingAccountIsKeptAcrossSignIns(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	if r := c.Post(t, "settings.profile", url.Values{"default_account": {User2}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	for _, account := range []string{User, User2} {
		other := a.SignIn(t, account)
		page := other.Page(t, "settings.profile")
		if got := SelectedValue(page, "default_account"); got != User2 {
			t.Fatalf("%s remembers default %q, want %q", account, got, User2)
		}
		// An unavailable default remains selectable and survives an unrelated save.
		if r := other.Post(t, "settings.profile", url.Values{"shown": {Shown(page)}, "default_account": {User2}, "messages_per_page": {"17"}}); r.StatusCode != http.StatusFound {
			t.Fatal(r.Status)
		}
	}
	if r := c.Post(t, "settings.profile", url.Values{"theme": {"ink"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if got := SelectedValue(c.Page(t, "settings.profile"), "default_account"); got != User2 {
		t.Fatalf("an absent field cleared default %q", got)
	}
	if r := c.Post(t, "settings.profile", url.Values{"default_account": {""}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if got := SelectedValue(c.Page(t, "settings.profile"), "default_account"); got != "" {
		t.Fatalf("clearing kept default %q", got)
	}
}

func TestDefaultOutgoingAccountRefusesAnUnknownAccount(t *testing.T) {
	c := Alborz(t).SignIn(t, User)
	r := c.Post(t, "settings.profile", url.Values{"default_account": {"unknown@example.org"}})
	if r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("choosing an account not signed in answered %s", r.Status)
	}
}

func TestDefaultOutgoingAccountSurvivesAnOlderSettingsForm(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	c.SignIn(t, User2)
	page := c.Page(t, "settings.profile")
	if r := c.Post(t, "settings.profile", url.Values{"default_account": {User2}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if r := c.Post(t, "settings.profile", url.Values{"shown": {Shown(page)}, "default_account": {""}, "messages_per_page": {"17"}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if got := SelectedValue(c.Page(t, "settings.profile"), "default_account"); got != User2 {
		t.Fatalf("saving an old form lost the newer default: %q", got)
	}
}

// An account joining with another default outgoing account is asked about it (ADR 52).
func TestSigningInAskedAboutAnotherDefaultAccount(t *testing.T) {
	a := Alborz(t)
	both := a.SignIn(t, User)
	both.SignIn(t, User2)
	if r := both.Post(t, "settings.profile", url.Values{"default_account": {User2}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}
	if r := a.SignIn(t, User).Post(t, "settings.profile", url.Values{"default_account": {""}}); r.StatusCode != http.StatusFound {
		t.Fatal(r.Status)
	}

	c := a.SignIn(t, User2)
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}}, url.Values{"add": {"1"}})
	bar := NoticeBar(c.Follow(t, resp.Header.Get("Location")))
	if !Says(t, bar, "settings.defaultaccount") || !Says(t, bar, "settings.firstaccount") || !strings.Contains(bar, User2) {
		t.Fatalf("no notice names the differing default account:\n%s", bar)
	}
	if got := SelectedValue(c.Page(t, "settings.profile"), "default_account"); got != User2 {
		t.Errorf("the session took the default %q before the reader answered", got)
	}
}

// The header's language menu sets the language alone and returns to the page it was chosen on.
func TestLanguageMenu(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	if resp := c.Post(t, "settings.profile", url.Values{"messages_per_page": {"17"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("saving the profile: %s", resp.Status)
	}
	// The URL's language would outrank the one chosen, so the way back drops it.
	resp := c.Post(t, "settings.language", url.Values{"language": {"de"}, "next": {a.Path("settings.sessions") + "?lang=fr"}})
	if want := a.Path("settings.sessions"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
		t.Fatalf("choosing the language: %s to %q, want %q", resp.Status, resp.Header.Get("Location"), want)
	}
	if page := c.Follow(t, resp.Header.Get("Location")); !strings.Contains(page, `<html lang="de"`) {
		t.Error("the next page is not in the language chosen")
	}
	if got := perPage(t, c); got != "17" {
		t.Errorf("choosing the language left a page size of %s, want 17", got)
	}
}

// Two browsers that read the profile before either saved each change their own choice and keep the other's.
func TestProfileSavesFromTwoBrowsersBothStay(t *testing.T) {
	a := Alborz(t)
	language, theme := a.SignIn(t, User), a.SignIn(t, User)
	var wg sync.WaitGroup
	wg.Go(func() {
		if resp := language.Post(t, "settings.profile", url.Values{"language": {"de"}}); resp.StatusCode != http.StatusFound {
			t.Errorf("choosing the language: %s", resp.Status)
		}
	})
	wg.Go(func() {
		if resp := theme.Post(t, "settings.profile", url.Values{"theme": {"ink"}}); resp.StatusCode != http.StatusFound {
			t.Errorf("choosing the theme: %s", resp.Status)
		}
	})
	wg.Wait()

	page := a.SignIn(t, User).Page(t, "settings.profile")
	if !strings.Contains(page, `<html lang="de"`) {
		t.Error("the language chosen in one browser is gone")
	}
	if got := SelectedValue(page, "theme"); got != "ink" {
		t.Errorf("the theme chosen in the other browser is %q, want ink", got)
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
			if !Says(t, bar, "settings.perpage") || !strings.Contains(bar, "17") || !strings.Contains(bar, "50") {
				t.Fatalf("no notice names the differing page size:\n%s", bar)
			}
			for range 3 {
				if got := perPage(t, c); got != "50" {
					t.Fatalf("the session took %s before the reader answered", got)
				}
			}

			var fields url.Values
			for _, f := range regexp.MustCompile(`(?s)<form[^>]*action="([^"]+)"[^>]*>(.*?)</form>`).FindAllStringSubmatch(bar, -1) {
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
			other := a.SignIn(t, User)
			if resp := other.Post(t, "settings.profile", url.Values{"messages_per_page": {"23"}}); resp.StatusCode != http.StatusFound {
				t.Fatalf("saving in another browser: %s", resp.Status)
			}
			if got := perPage(t, c); got != "23" {
				t.Errorf("after answering, another browser's update reads %s, want 23", got)
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
	warned := 0
	if Says(t, NoticeBar(c.Follow(t, signIn.Header.Get("Location"))), "notice.misread") {
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
		if Says(t, NoticeBar(resp.Text), "notice.misread") {
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
