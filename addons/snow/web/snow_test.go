package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestSnowFallsOnlyForAReaderWhoAsked(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	script := `<script defer src="` + a.Path("snow.script") + `"></script>`
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="main"`) || strings.Contains(page, script) {
		t.Fatal("snow falls for a reader who did not ask")
	}
	if resp := c.Post(t, "settings.profile", url.Values{"addon": {"snow"}}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning snow on answered %s", resp.Status)
	}
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, script) {
		t.Fatal("the page does not load the snow")
	}
	resp := c.Get(t, "snow.script")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || !strings.Contains(resp.Text, "letItSnow") {
		t.Fatalf("the script answered %s, %s", resp.Status, resp.Header.Get("Content-Type"))
	}
}
