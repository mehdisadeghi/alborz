package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	. "git.mehdix.org/alborz/internal/alborztest"
)

func rememberedBrowser(t *testing.T, a *App) (*Client, *alborz.SessionRecord, *Response) {
	t.Helper()
	c := a.Client()
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatal(resp.Status)
	}
	var id string
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "alborz_session" {
			id, _, _ = strings.Cut(cookie.Value, ".")
		}
	}
	rec, ok := a.Server.SessionRecords.Load(id)
	if !ok {
		t.Fatal("remembered session was not saved")
	}
	return c, rec, resp
}

func TestRememberedSignInSetsOneSixtyDayDeadline(t *testing.T) {
	a := Alborz(t)
	_, rec, resp := rememberedBrowser(t, a)
	if left := time.Until(rec.Expires); left < 60*24*time.Hour-time.Minute || left > 60*24*time.Hour {
		t.Fatalf("remembered session expires in %v", left)
	}
	var expires time.Time
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "alborz_session" {
			expires = cookie.Expires
		}
	}
	if !expires.Equal(rec.Expires.Truncate(time.Second)) {
		t.Fatalf("cookie %s differs from saved expiry %s", expires, rec.Expires)
	}
}

func TestRememberedUserActivityRenewsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	a := Alborz(t, DataDir(dir))
	c, rec, _ := rememberedBrowser(t, a)
	rec.Created = time.Now().Add(-90 * 24 * time.Hour)
	rec.Seen = time.Now().Add(-90 * 24 * time.Hour)
	rec.Expires = time.Now().Add(10 * 24 * time.Hour)
	if err := a.Server.SessionRecords.Save(rec); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b := Alborz(t, DataDir(dir), WithIMAP(a.IMAP))
	resumed := b.Client()
	resumed.HTTP.Jar = c.HTTP.Jar
	resp := resumed.Get(t, "settings.profile")
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	kept, ok := b.Server.SessionRecords.Load(rec.ID)
	if !ok || time.Until(kept.Expires) < 60*24*time.Hour-time.Minute {
		t.Fatal("authenticated activity did not renew the persisted session")
	}
	var cookieExpiry time.Time
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "alborz_session" {
			cookieExpiry = cookie.Expires
		}
	}
	if !cookieExpiry.Equal(kept.Expires.Truncate(time.Second)) {
		t.Fatalf("renewed cookie %s differs from saved expiry %s", cookieExpiry, kept.Expires)
	}
	b.Close()
	d := Alborz(t, DataDir(dir), WithIMAP(a.IMAP))
	again := d.Client()
	again.HTTP.Jar = resumed.HTTP.Jar
	if resp := again.Get(t, "settings.profile"); resp.StatusCode != http.StatusOK {
		t.Fatalf("renewed session did not survive another restart: %s", resp.Status)
	}
}

func TestAutomaticRequestsDoNotRenewRememberedSignIn(t *testing.T) {
	for _, tc := range []struct {
		name, route string
		args        []any
		unasked     bool
	}{
		{"poll", "settings.profile", nil, true},
		{"lock state", "auth.unlock.state", nil, false},
		{"status", "core.status", []any{"date-gregorian"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a := Alborz(t, DataDir(dir))
			c, rec, _ := rememberedBrowser(t, a)
			rec.Seen = time.Now().Add(-24 * time.Hour).Truncate(time.Second)
			rec.Expires = rec.Seen.Add(60 * 24 * time.Hour)
			if err := a.Server.SessionRecords.Save(rec); err != nil {
				t.Fatal(err)
			}
			a.Close()
			b := Alborz(t, DataDir(dir), WithIMAP(a.IMAP))
			resumed := b.Client()
			resumed.HTTP.Jar = c.HTTP.Jar
			req, err := http.NewRequest(http.MethodGet, b.URL+b.Path(tc.route, tc.args...), nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.unasked {
				req.Header.Set("Alborz-Unasked", "1")
			}
			resp := resumed.Do(t, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatal(resp.Status)
			}
			for _, cookie := range resp.Cookies() {
				if cookie.Name == "alborz_session" {
					t.Fatal("automatic request renewed the cookie")
				}
			}
			kept, ok := b.Server.SessionRecords.Load(rec.ID)
			if !ok || !kept.Expires.Equal(rec.Expires) || !kept.Seen.Equal(rec.Seen) {
				t.Fatal("automatic request renewed the stored session")
			}
		})
	}
}
