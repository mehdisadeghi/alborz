package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

func TestBoostedSignInLoadsTheSessionScopedHead(t *testing.T) {
	a := Alborz(t)
	c := a.Client()
	form := url.Values{"username": {User}, "password": {Password}}
	req, _ := http.NewRequest(http.MethodPost, a.URL+a.Path("auth.login"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Boosted", "true")
	resp := c.Do(t, req)
	to := resp.Header.Get("HX-Redirect")
	if resp.StatusCode != http.StatusOK || to == "" {
		t.Fatalf("sign-in retained its public head: %s %q", resp.Status, to)
	}
	page := c.Follow(t, to)
	cookie := c.Cookie("alborz_session")
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	id, secret, _ := strings.Cut(cookie.Value, ".")
	if !strings.Contains(page, `name="alborz-session" content="`+id+`"`) || !strings.Contains(page, "session-lock.js") {
		t.Fatal("authenticated head lacks session guard")
	}
	if strings.Contains(page, secret) {
		t.Fatal("session guard exposed the cookie secret")
	}
	if strings.Contains(page, `class="nav-session-lock"`) {
		t.Fatal("the header offers session lock without a configured passkey")
	}
	if !strings.Contains(c.Page(t, "settings.passkeys"), a.Path("settings.passkeys.create")) {
		t.Fatal("passkey setup is unavailable in settings")
	}
}

func TestSignInRejectsNonLocalReturnPaths(t *testing.T) {
	a := Alborz(t)
	for _, boosted := range []bool{false, true} {
		mode := "native"
		if boosted {
			mode = "boosted"
		}
		t.Run(mode, func(t *testing.T) {
			signIn := func(next string) string {
				t.Helper()
				form := url.Values{"username": {User}, "password": {Password}, "next": {next}}
				req, _ := http.NewRequest(http.MethodPost, a.URL+a.Path("auth.login"), strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				status, header := http.StatusFound, "Location"
				if boosted {
					req.Header.Set("HX-Request", "true")
					req.Header.Set("HX-Boosted", "true")
					status, header = http.StatusOK, "HX-Redirect"
				}
				resp := a.Client().Do(t, req)
				if resp.StatusCode != status {
					t.Fatalf("sign-in: %s", resp.Status)
				}
				return resp.Header.Get(header)
			}
			fallback := signIn("")
			if fallback == "" {
				t.Fatal("sign-in has no default destination")
			}
			for _, next := range []string{"http:/evil.example/path", "javascript:/a/path", "///evil.example/path", "////evil.example/path", "/%2Fevil.example/path"} {
				if got := signIn(next); got != fallback {
					t.Errorf("return path %q redirected to %q instead of %q", next, got, fallback)
				}
			}
		})
	}
}
