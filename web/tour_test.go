package web_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

var tourSteps = regexp.MustCompile(`<section class="tour-step" data-stop="([a-z]+)"[^>]*>`)

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

// A mobile visit cannot consume the first-tour offer; only a displayed desktop tour acknowledges it.
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
	if _, pending := signIn(); !strings.Contains(pending, "tour=") {
		t.Errorf("an undisplayed tour consumed the offer: %s", pending)
	}
	if resp := c.Post(t, "core.tour.seen", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("acknowledging the displayed tour answered %s", resp.Status)
	}
	if _, again := signIn(); strings.Contains(again, "tour=") {
		t.Errorf("a second sign-in opened the tour again at %s", again)
	}
}

func TestTourAcknowledgementRejectsCrossOrigin(t *testing.T) {
	a := Alborz(t)
	c := a.SignIn(t, User)
	req, err := http.NewRequest(http.MethodPost, a.URL+a.Path("core.tour.seen"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp := c.Do(t, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin acknowledgement answered %s", resp.Status)
	}
	login := a.Client().Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}})
	if !strings.Contains(login.Header.Get("Location"), "tour=") {
		t.Fatal("a refused acknowledgement consumed the tour offer")
	}
}
