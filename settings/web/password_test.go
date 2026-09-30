package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func change(current, next, confirm string) url.Values {
	return url.Values{"current": {current}, "new": {next}, "confirm": {confirm}}
}

// The browser that changed the password stays signed in and remembers the new one across a
// restart; every other browser of the account is signed out.
func TestPasswordChange(t *testing.T) {
	imap, dir := IMAP(t), t.TempDir()
	opts := []Option{WithIMAP(imap), WithPasswd(Passwd(t, imap)), DataDir(dir)}
	a := Alborz(t, opts...)
	here := a.Client()
	if resp := here.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in: %s", resp.Status)
	}
	other := a.SignIn(t, User)

	resp := here.Post(t, "settings.password", change(Password, "fresh-secret", "fresh-secret"))
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("changing the password: %s\n%s", resp.Status, NoticeBar(resp.Text))
	}
	if bar := NoticeBar(here.Follow(t, resp.Header.Get("Location"))); !strings.Contains(bar, "Password changed.") {
		t.Fatalf("no notice says the password changed:\n%s", bar)
	}
	if to := other.Get(t, "settings.password").Header.Get("Location"); !strings.HasPrefix(to, a.Path("auth.login")) {
		t.Fatalf("the other browser was not signed out; it was sent to %q", to)
	}
	if resp := here.Get(t, "settings.password"); resp.StatusCode != http.StatusOK {
		t.Fatalf("this browser after the change: %s", resp.Status)
	}

	a.Close()
	a = Alborz(t, opts...)
	back := a.Client()
	back.HTTP.Jar = here.HTTP.Jar
	if page := back.Page(t, "settings.password"); !strings.Contains(page, `name="current"`) {
		t.Fatalf("after a restart the remembered login did not come back:\n%s", NoticeBar(page))
	}
}

func TestPasswordChangeRefused(t *testing.T) {
	imap := IMAP(t)
	for _, c := range []struct {
		name   string
		opts   []Option
		form   url.Values
		status int
		alert  string
	}{
		{"wrong current", []Option{WithPasswd(Passwd(t, imap))}, change("wrong", "fresh-secret", "fresh-secret"),
			http.StatusUnprocessableEntity, "The server said: Old password is incorrect"},
		{"policy", []Option{WithPasswd(Passwd(t, imap))}, change(Password, "abc", "abc"),
			http.StatusUnprocessableEntity, "The server said: Server pam_chauthtok error 20, password not changed"},
		{"mismatch", []Option{WithPasswd(Passwd(t, imap))}, change(Password, "fresh-secret", "fresh-secrets"),
			http.StatusUnprocessableEntity, "The new passwords do not match."},
		{"no server", nil, change(Password, "fresh-secret", "fresh-secret"),
			http.StatusUnprocessableEntity, "Password changes are not set up for " + Domain},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := Alborz(t, append(c.opts, WithIMAP(imap))...)
			resp := a.SignIn(t, User).Post(t, "settings.password", c.form)
			if resp.StatusCode != c.status || !strings.Contains(NoticeBar(resp.Text), c.alert) {
				t.Fatalf("%s, want %d with %q:\n%s", resp.Status, c.status, c.alert, NoticeBar(resp.Text))
			}
			if !strings.Contains(resp.Text, `name="current"`) {
				t.Fatal("the refusal did not render the form")
			}
		})
	}
}
