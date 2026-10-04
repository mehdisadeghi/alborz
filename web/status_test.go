package web_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/web"
)

func turnOn(t *testing.T, c *Client, segments ...string) {
	t.Helper()
	if resp := c.Post(t, "settings.profile", url.Values{"status": segments}); resp.StatusCode != http.StatusFound {
		t.Fatalf("turning on %v answered %s", segments, resp.Status)
	}
}

func TestTheStatusBarShowsOnlyWhatWasTurnedOn(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `id="main"`) || strings.Contains(page, "status-bar") {
		t.Fatal("a reader who turned nothing on has a status bar")
	}

	turnOn(t, c, "date-gregorian")
	page := c.Page(t, "mail.mailbox", "INBOX")
	if !strings.Contains(page, `hx-get="`+a.Path("core.status", "date-gregorian")+`"`) {
		t.Fatal("the bar does not ask for the segment turned on")
	}
	for _, off := range []string{"mail", "date-shcal"} {
		if strings.Contains(page, `hx-get="`+a.Path("core.status", off)+`"`) {
			t.Fatalf("the bar asks for %s, left off", off)
		}
	}

	now := time.Now().UTC()
	gregorian := c.Page(t, "core.status", "date-gregorian")
	for _, want := range []string{now.Weekday().String(), fmt.Sprintf("%d %s %d", now.Day(), now.Month(), now.Year())} {
		if !strings.Contains(gregorian, want) {
			t.Errorf("the Gregorian date does not say %s:\n%s", want, gregorian)
		}
	}

	turnOn(t, c, "date-gregorian", "date-shcal")
	shcal := c.Page(t, "core.status", "date-shcal")
	// The Solar Hijri year is 621 or 622 behind, as the day falls before or after Nowruz.
	if !strings.Contains(shcal, fmt.Sprint(now.Year()-621)) && !strings.Contains(shcal, fmt.Sprint(now.Year()-622)) {
		t.Errorf("the Solar Hijri date holds no Solar Hijri year:\n%s", shcal)
	}
	if strings.Contains(shcal, now.Weekday().String()) {
		t.Errorf("the second date repeats the weekday:\n%s", shcal)
	}
}

func TestASegmentPastItsTimeShowsADash(t *testing.T) {
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	a := Alborz(t, Segments(web.Segment{Name: "stuck", Show: func(*web.Context) (web.Shown, error) {
		<-stuck
		return web.Shown{Parts: []string{"late"}}, nil
	}}))
	c := a.SignIn(t, User)
	turnOn(t, c, "stuck")
	start := time.Now()
	shown := strings.TrimSpace(c.Page(t, "core.status", "stuck"))
	if shown != "–" {
		t.Errorf("a segment that never answered shows %q", shown)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("the bar waited %s for it", waited)
	}
}

// A page asks its segments again every minute; were that the reader acting, an open tab would never lock.
func TestPollingIsNotTheReaderActing(t *testing.T) {
	a := Alborz(t)
	c := a.Client()
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in answered %s", resp.Status)
	}
	turnOn(t, c, "date-gregorian")
	if page := c.Page(t, "mail.mailbox", "INBOX"); !strings.Contains(page, `hx-headers='{"Alborz-Unasked": "1"}'`) {
		t.Fatal("the bar's segments do not say the page asked by itself")
	}
	session := c.Cookie("alborz_session")
	if session == nil {
		t.Fatal("signing in set no session cookie")
	}
	id, _, _ := strings.Cut(session.Value, ".")
	device := func() string {
		rec, ok := a.Server.SessionRecords.Load(id)
		if !ok {
			t.Fatal("signing in with remember-me kept no session")
		}
		return rec.Device
	}

	ask := func(header http.Header) {
		req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("core.status", "date-gregorian"), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = header
		if resp := c.Do(t, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("the segment answered %s", resp.Status)
		}
	}
	ask(http.Header{"User-Agent": {"poller"}, "Alborz-Unasked": {"1"}})
	if got := device(); got == "poller" {
		t.Fatal("a poll was taken for the reader acting")
	}
	ask(http.Header{"User-Agent": {"poller"}})
	if got := device(); got != "poller" {
		t.Fatalf("the reader asking was not noted, the session holds %q; the check above proves nothing", got)
	}
}

func TestAPollFromASignedOutPageDrawsNothing(t *testing.T) {
	a := Alborz(t)
	req, err := http.NewRequest(http.MethodGet, a.URL+a.Path("core.status", "date-gregorian"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := a.Client()
	if resp := c.Do(t, req); resp.StatusCode != http.StatusFound {
		t.Fatalf("a signed-out reader asking answered %s, want the way to sign in", resp.Status)
	}
	req.Header.Set("Alborz-Unasked", "1")
	if resp := c.Do(t, req); resp.StatusCode != http.StatusNoContent || resp.Text != "" {
		t.Errorf("a signed-out page's poll answered %s with %d bytes", resp.Status, len(resp.Text))
	}
}

// htmx finds a preserved element, and some targets, by '#'+id unescaped: an id an address went into whole
// is no selector, and the swap stops half done.
func TestIdsHtmxSelectAreCSSIdentifiers(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	turnOn(t, c, "mail", "date-gregorian")
	doc, err := html.Parse(strings.NewReader(c.Follow(t, a.Path("mail.mailbox", "INBOX")+"?account="+User)))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for n := range doc.Descendants() {
		for _, attr := range n.Attr {
			switch {
			case attr.Key == "hx-preserve":
				ids = append(ids, attrOf(n, "id"))
			case attr.Key == "hx-target" && strings.HasPrefix(attr.Val, "#"):
				ids = append(ids, attr.Val[1:])
			}
		}
	}
	if len(ids) == 0 {
		t.Fatal("no preserved or targeted ids on the page; the check is vacuous")
	}
	ident := regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_-]*$`)
	for _, id := range ids {
		if !ident.MatchString(id) {
			t.Errorf("id %q is not a CSS identifier", id)
		}
	}
}

func attrOf(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}
