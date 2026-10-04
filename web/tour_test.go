package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
	"git.mehdix.org/alborz/internal/locale"
)

var (
	tourSteps = regexp.MustCompile(`<section class="tour-step" data-stop="([a-z]+)"[^>]*>`)
	tourNext  = regexp.MustCompile(`<a class="[^"]*tour-next" href="([^"]+)" data-stop="([a-z]+)">`)
	tourKey   = regexp.MustCompile(`tour\.[a-z]+\.(?:title|body)`)
)

// shownStop is the stop a page draws the tour open at, "" for no tour.
func shownStop(t *testing.T, page string) string {
	t.Helper()
	shown := ""
	for _, m := range tourSteps.FindAllStringSubmatch(page, -1) {
		if !strings.Contains(m[0], " hidden") {
			if shown != "" {
				t.Fatalf("the tour shows %s and %s at once", shown, m[1])
			}
			shown = m[1]
		}
	}
	return shown
}

// The accounts menu opens the tour, and every stop's Next opens the next
// stop on its own page, in every language.
func TestTourWalk(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	plain := c.Page(t, "settings.profile")
	if got := shownStop(t, plain); got != "" {
		t.Fatalf("a page asked for without the tour shows it at %s", got)
	}
	take := regexp.MustCompile(`<a href="([^"]+)">` + regexp.QuoteMeta(T(t, "tour.take")) + `</a>`).FindStringSubmatch(plain)
	if take == nil {
		t.Fatal("the accounts menu offers no tour")
	}
	page := c.Follow(t, take[1])
	stops := tourSteps.FindAllStringSubmatch(page, -1)
	if len(stops) < 2 {
		t.Fatalf("%s opened no tour", take[1])
	}
	if got := shownStop(t, page); got != stops[0][1] {
		t.Fatalf("%s opened the tour at %q, want %q", take[1], got, stops[0][1])
	}

	// Another browser reads each stop in every language: ?lang= sticks.
	reader := a.SignIn(t, User2)
	for _, stop := range stops[1:] {
		var next string
		for _, link := range tourNext.FindAllStringSubmatch(page, -1) {
			if link[2] == stop[1] {
				next = link[1]
			}
		}
		if next == "" {
			t.Fatalf("no Next leads to %s", stop[1])
		}
		page = c.Follow(t, next)
		if got := shownStop(t, page); got != stop[1] {
			t.Fatalf("%s shows the stop %q, want %q", next, got, stop[1])
		}
		for _, lang := range locale.Languages {
			if key := tourKey.FindString(reader.Follow(t, next+"&lang="+lang)); key != "" {
				t.Errorf("%s has no %s", lang, key)
			}
		}
	}
}

// The tour opens by itself at a person's first sign-in only, whichever browser signs in next.
func TestTourOpensOnceAtSignIn(t *testing.T) {
	a := Alborz(t)
	signIn := func() (*Client, string) {
		c := a.Client()
		resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}})
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("signing in answered %s", resp.Status)
		}
		return c, resp.Header.Get("Location")
	}
	c, first := signIn()
	if got := shownStop(t, c.Follow(t, first)); got == "" {
		t.Fatalf("the first sign-in landed on %s, with no tour", first)
	}
	if _, again := signIn(); strings.Contains(again, "tour=") {
		t.Errorf("a second sign-in opened the tour again at %s", again)
	}
}
