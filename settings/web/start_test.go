package web_test

import (
	"net/http"
	"net/url"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestDefaultPageFollowsTheProfileWithoutChangingDirectLinks(t *testing.T) {
	a, c := SignedIn(t, User)
	if r := c.Post(t, "core.tour.seen", nil); r.StatusCode != http.StatusNoContent {
		t.Fatal(r.Status)
	}
	for _, tc := range []struct {
		page, route string
		args        []any
	}{
		{"mail", "mail.mailbox", []any{"INBOX"}},
		{"calendar", "calendar.month", nil},
		{"contacts", "contacts.list", nil},
		{"tasks", "calendar.tasks", nil},
		{"journal", "calendar.journal", nil},
	} {
		if r := c.Post(t, "settings.profile", url.Values{"start_page": {tc.page}}); r.StatusCode != http.StatusFound {
			t.Fatalf("saving %s: %s", tc.page, r.Status)
		}
		if got, want := c.Get(t, "mail.home").Header.Get("Location"), a.Path(tc.route, tc.args...); got != want {
			t.Fatalf("default %s opened %q, want %q", tc.page, got, want)
		}
		if r := c.Get(t, "mail.mailbox", "INBOX"); r.StatusCode != http.StatusOK {
			t.Fatalf("direct mail link changed for %s: %s", tc.page, r.Status)
		}
		if got, want := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}}).Header.Get("Location"), a.Path(tc.route, tc.args...); got != want {
			t.Fatalf("login default %s opened %q, want %q", tc.page, got, want)
		}
	}
	if r := c.Post(t, "settings.profile", url.Values{"start_page": {"https://elsewhere.invalid"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid default page accepted: %s", r.Status)
	}
}
