package calendar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-ical"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

// The production client refuses loopback, so the tests inject a plain one.
func feedsAt(t *testing.T, file string, client *http.Client) *feeds {
	t.Helper()
	db, err := bolt.Open(file, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &feeds{client: client, log: echo.New().Logger, db: db, key: feedsKey, held: map[string]*heldFeed{}}
}

var feedsKey = func() *fernet.Key {
	var k fernet.Key
	if err := k.Generate(); err != nil {
		panic(err)
	}
	return &k
}()

func TestFeedSurvivesARestartAndRefreshesOnItsOwnClock(t *testing.T) {
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nREFRESH-INTERVAL;VALUE=DURATION:PT2H\r\n" +
		"BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nSUMMARY:New Year\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	const modified = "Wed, 01 Jan 2026 00:00:00 GMT"
	var hits int
	var asked http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		asked = r.Header.Clone()
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", modified)
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(ics))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "feeds.db")

	before := feedsAt(t, file, srv.Client())
	if _, err := before.refresh(context.Background(), srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	before.db.Close()

	f := feedsAt(t, file, srv.Client())
	cal := f.calendar(srv.URL)
	if cal == nil || len(cal.Events()) != 1 || hits != 1 {
		t.Fatalf("after a restart the feed served %v after %d requests, want its one event after 1", cal, hits)
	}
	now := time.Now()
	f.round(context.Background(), now.Add(time.Hour))
	if hits != 1 {
		t.Fatalf("the feed was fetched an hour after it asked for two")
	}
	f.round(context.Background(), now.Add(3*time.Hour))
	if hits != 2 || asked.Get("If-None-Match") != `"v1"` || asked.Get("If-Modified-Since") != modified {
		t.Fatalf("the due fetch was request %d with validators %q %q, want request 2 conditional on the stored ones",
			hits, asked.Get("If-None-Match"), asked.Get("If-Modified-Since"))
	}
	if cal := f.calendar(srv.URL); cal == nil || len(cal.Events()) != 1 {
		t.Fatal("the feed lost its events after a not-modified refresh")
	}
}

func TestFeedIntervalWithinBounds(t *testing.T) {
	for prop, want := range map[string]time.Duration{
		"":                                     feedEvery,
		"REFRESH-INTERVAL;VALUE=DURATION:PT6H": 6 * time.Hour,
		"REFRESH-INTERVAL;VALUE=DURATION:P1W":  feedCeiling,
		"REFRESH-INTERVAL;VALUE=DURATION:PT1M": feedFloor,
		"X-PUBLISHED-TTL:PT3H":                 3 * time.Hour,
	} {
		raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"
		if prop != "" {
			raw += prop + "\r\n"
		}
		raw += "BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		cal, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
		if err != nil {
			t.Fatal(err)
		}
		if got := refreshEvery(cal); got != want {
			t.Errorf("%q: every %v, want %v", prop, got, want)
		}
	}
}

func TestSubscriptionIsACalendarInTheRail(t *testing.T) {
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nSUMMARY:New Year\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(ics))
	}))
	defer srv.Close()

	sub := Subscription{URL: srv.URL, Name: "Holidays", Color: "#123456"}
	if !isSubscription(sub.path()) || sub.path() == (Subscription{URL: srv.URL + "/other"}).path() {
		t.Fatalf("pseudo-path %q does not identify the feed", sub.path())
	}
	if i := subscriptionAt([]Subscription{sub}, sub.path()); i != 0 {
		t.Fatalf("the subscription is not found by its own path: %d", i)
	}

	info := sub.info("a@test.local")
	if info.Address != srv.URL || info.Writable || !supportsEvent(info.Components) || supportsTodo(info.Components) {
		t.Fatalf("rail entry: %+v", info)
	}
	p := &plugin{feeds: feedsAt(t, filepath.Join(t.TempDir(), "feeds.db"), srv.Client())}
	if _, err := p.feeds.refresh(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	info.Shown = false
	if got := p.subscriptionObjects([]dav.Collection{info}, ""); len(got) != 0 {
		t.Errorf("a hidden subscription drew %d objects", len(got))
	}
	info.Shown = true
	got := p.subscriptionObjects([]dav.Collection{info}, "")
	if len(got) != 1 || !got[0].ReadOnly || got[0].Color != "#123456" {
		t.Fatalf("a visible subscription drew %+v", got)
	}
}

func TestNormalizeFeedURL(t *testing.T) {
	for in, want := range map[string]string{
		"webcal://cal.example/a.ics":  "https://cal.example/a.ics",
		"//cal.example/a.ics":         "https://cal.example/a.ics",
		"cal.example/a.ics":           "https://cal.example/a.ics",
		" https://cal.example/a.ics ": "https://cal.example/a.ics",
	} {
		if got := normalizeFeedURL(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
