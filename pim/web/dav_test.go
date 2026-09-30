package web_test

import (
	"bytes"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "git.mehdix.org/alborz/internal/alborztest"
)

// A DELETE carrying If-Match must match the object's tag (RFC 9110 13.1.1).
func TestADeleteWithAStaleTagKeepsTheObject(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Plans", "")
	a.Put(t, cal+"dentist.ics", "text/calendar", Event("dentist", "Dentist"))

	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": `"stale"`}, ""); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a delete with a stale tag answered %s", resp.Status)
	}
	resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the object is gone after a refused delete: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodDelete, cal+"dentist.ics", map[string]string{"If-Match": resp.Header.Get("ETag")}, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("a delete with the object's own tag answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"dentist.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the deleted object is still there: %s", resp.Status)
	}
}

// One UID per calendar (RFC 4791 5.3.2.1).
func TestASecondObjectWithATakenUIDIsRefused(t *testing.T) {
	a := Alborz(t)
	cal := a.SignIn(t, User).CreateCalendar(t, "Work", "")
	a.Put(t, cal+"review.ics", "text/calendar", Event("review", "Review"))
	ics := map[string]string{"Content-Type": "text/calendar"}

	if resp := a.DAV(t, http.MethodPut, cal+"copy.ics", ics, Event("review", "Review again")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second object with a taken UID answered %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodGet, cal+"copy.ics", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused object was written anyway: %s", resp.Status)
	}
	if resp := a.DAV(t, http.MethodPut, cal+"review.ics", ics, Event("review", "Review, moved")); resp.StatusCode/100 != 2 {
		t.Fatalf("rewriting the object that holds the UID answered %s", resp.Status)
	}
	if kept := a.DAV(t, http.MethodGet, cal+"review.ics", nil, "").Text; !strings.Contains(kept, "Review, moved") {
		t.Errorf("the rewrite did not stick: %s", kept)
	}
}

// Force refresh lists what another client added on a remote server.
func TestACalendarCreatedElsewhereIsListedOnRefresh(t *testing.T) {
	d := DAV(t, Calendar(DAVHome+"personal/", "Personal"))
	c := Alborz(t, WithDAV(d)).SignIn(t, User)
	work := d.At() + DAVHome + "work/"
	if rail := c.Page(t, "calendar.calendars.create"); strings.Contains(rail, work) {
		t.Fatalf("%s is listed before it is made", work)
	}
	d.Apply(Calendar(DAVHome+"work/", "Work"))
	if resp := c.Post(t, "calendar.refresh", url.Values{}); resp.StatusCode != http.StatusFound {
		t.Fatalf("refreshing answered %s", resp.Status)
	}
	if rail := c.Page(t, "calendar.calendars.create"); !strings.Contains(rail, work) {
		t.Errorf("%s is not listed after asking the server again", work)
	}
}

// Whoever reads alborz.db or the cache without the login key learns no address, subject or summary (ADR 65).
func TestNothingKeptOnDiskIsPlain(t *testing.T) {
	imap := IMAP(t)
	other := Alborz(t, WithIMAP(imap))
	remote := other.SignIn(t, User).CreateCalendar(t, "Remote", "")
	other.Put(t, remote+"offsite.ics", "text/calendar", Event("offsite", "Offsite planning"))

	a := Alborz(t, WithIMAP(imap), other.DAVUpstream())
	c := a.Client()
	resp := c.Post(t, "auth.login", url.Values{"username": {User}, "password": {Password}, "remember-me": {"on"}})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("signing in answered %s", resp.Status)
	}
	local := c.CreateCalendar(t, "Local", "")
	a.Put(t, local+"review.ics", "text/calendar", Event("review", "Budget review"))
	if page := c.Page(t, "mail.message", "INBOX", 1); !strings.Contains(page, "Ideas for the redesign") {
		t.Fatal("the first message is not the one this test names")
	}
	if page := c.Page(t, "calendar.month", url.Values{"month": {"2026-09"}}); !strings.Contains(page, "Offsite planning") || !strings.Contains(page, "Budget review") {
		t.Fatal("the month shows neither event; the files hold nothing to look for")
	}
	a.Close()

	files := map[string][]byte{}
	for _, dir := range []string{a.Server.DataPath, a.Server.Options.CacheDir} {
		err := filepath.WalkDir(filepath.Dir(dir+"/"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			files[p], err = os.ReadFile(p)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, held := range []struct{ file, key string }{
		{"alborz.db", "alborz.pim/objects/"}, {"alborz.db", "alborz.sessions/"}, {"caldav.db", "dav.mirror/objects/"},
	} {
		found := false
		for p, b := range files {
			found = found || filepath.Base(p) == held.file && bytes.Contains(b, []byte(held.key))
		}
		if !found {
			t.Fatalf("no %s holds %s: the scan below reads nothing", held.file, held.key)
		}
	}
	for p, b := range files {
		for _, plain := range []string{User, "Ideas for the redesign", "Budget review", "Offsite planning", "Local"} {
			for _, form := range spellings(plain) {
				if bytes.Contains(b, []byte(form)) {
					t.Errorf("%s holds %q in the clear, as %q", p, plain, form)
				}
			}
		}
	}
}

// spellings are s and its base64 at each of the three offsets it may start at, as JSON writes []byte.
func spellings(s string) []string {
	out := []string{s}
	for pad := range 3 {
		e := strings.TrimRight(base64.StdEncoding.EncodeToString(append(make([]byte, pad), s...)), "=")
		out = append(out, e[(pad*8+5)/6:len(e)-2])
	}
	return out
}
