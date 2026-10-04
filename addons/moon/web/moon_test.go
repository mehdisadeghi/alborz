package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestTheMoonShowsOnlyForAReaderWhoAsked(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="main"`) || strings.Contains(page, `id="status-moon-`) {
		t.Fatal("the moon shows for a reader who did not ask")
	}
	if resp := c.Post(t, "settings.profile", url.Values{"status": {"moon"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning the moon on answered %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="status-moon-`) {
		t.Fatal("the status bar has no place for the moon")
	}
	shown := c.Page(t, "core.status", "moon")
	if !strings.ContainsAny(shown, "🌑🌒🌓🌔🌕🌖🌗🌘") || strings.Contains(shown, "moon.phase.") || strings.Contains(shown, "moon.lit") {
		t.Fatalf("the segment shows no named phase:\n%s", shown)
	}
}
