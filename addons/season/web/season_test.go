package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestSeasonFallsOnlyForAReaderWhoAsked(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	script := `<script defer src="` + a.Path("season.script") + `"></script>`
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="main"`) || strings.Contains(page, script) {
		t.Fatal("the season falls for a reader who did not ask")
	}
	if resp := c.Post(t, "settings.profile", url.Values{"addon": {"season"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning the season on answered %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, script) {
		t.Fatal("the page does not load the season")
	}
	for _, resp := range []*Response{c.Get(t, "season.script"), c.Get(t, "season.kind", "snow.js")} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || !strings.Contains(resp.Text, "@license-end") {
			t.Fatalf("a script answered %s, %s", resp.Status, resp.Header.Get("Content-Type"))
		}
	}
}
