package web_test

import (
	"encoding/json"
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
	if shown == "" || strings.Contains(shown, "late") {
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

	// The bar tells the browser to ask as the page, not the reader. Take the
	// declaration from the page itself: a page that stops declaring it fails
	// here, and a server that stops honouring it fails below.
	page := c.Page(t, "mail.mailbox", "INBOX")
	declared := http.Header{}
	for _, m := range regexp.MustCompile(`hx-(?:headers|request-headers)=(?:'([^']*)'|"([^"]*)")`).FindAllStringSubmatch(page, -1) {
		value := m[1] + m[2]
		var headers map[string]string
		if json.Unmarshal([]byte(html.UnescapeString(value)), &headers) != nil {
			continue
		}
		if headers["Alborz-Unasked"] == "1" {
			for k, v := range headers {
				declared.Set(k, v)
			}
			break
		}
	}
	if len(declared) == 0 {
		t.Fatal("no segment declares the page's self-asked request")
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
	ask(http.Header{"User-Agent": {"reader"}})
	if got := device(); got != "reader" {
		t.Fatalf("the reader asking was not noted, the session holds %q", got)
	}
	poll := declared.Clone()
	poll.Set("User-Agent", "poller")
	ask(poll)
	if got := device(); got != "reader" {
		t.Fatalf("a declared poll was taken for the reader acting, the session holds %q", got)
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
