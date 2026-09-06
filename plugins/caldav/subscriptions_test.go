package alborzcaldav

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A subscription's feed is fetched, parsed, and served as read-only,
// coloured events; a second fetch with the server's ETag costs no
// re-parse. The production client refuses loopback, so the test injects
// a plain one to exercise the fetch and parse against real ICS bytes.
func TestSubscriptionFetchParseAndServe(t *testing.T) {
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nSUMMARY:New Year\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == "\"v1\"" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", "\"v1\"")
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(ics))
	}))
	defer srv.Close()

	c := &subCache{entries: map[string]*subEntry{}, client: srv.Client()}
	if err := c.refresh(srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	obj, ok := c.events(srv.URL, "#123456")
	if !ok {
		t.Fatal("the fetched feed served no events")
	}
	if !obj.ReadOnly || obj.Color != "#123456" {
		t.Fatalf("feed object is read-only=%v color=%q, want true #123456", obj.ReadOnly, obj.Color)
	}
	if got := len(obj.Data.Events()); got != 1 {
		t.Fatalf("the feed served %d events, want 1", got)
	}
	if !c.fresh(srv.URL) {
		t.Fatal("a just-fetched feed is not fresh")
	}
	// A second refresh sends the ETag; the server answers 304 and the
	// events survive.
	if err := c.refresh(srv.URL); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if hits != 2 {
		t.Fatalf("server saw %d requests, want 2", hits)
	}
	if _, ok := c.events(srv.URL, "#123456"); !ok {
		t.Fatal("the feed lost its events after a not-modified refresh")
	}
}

// A subscription stands in the rail as a read-only calendar of events
// under a stable pseudo-path, and its events follow that entry's
// checkbox: hidden means not drawn. The production client refuses a
// loopback feed, so the cache is pointed at the test server for this.
func TestSubscriptionIsACalendarInTheRail(t *testing.T) {
	const ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nSUMMARY:New Year\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(ics))
	}))
	defer srv.Close()
	saved := subs.client
	subs.client = srv.Client()
	defer func() { subs.client = saved }()

	sub := Subscription{URL: srv.URL, Name: "Holidays", Color: "#123456"}
	if !isSubscription(sub.path()) || sub.path() == (Subscription{URL: srv.URL + "/other"}).path() {
		t.Fatalf("pseudo-path %q does not identify the feed", sub.path())
	}
	if i := subscriptionAt([]Subscription{sub}, sub.path()); i != 0 {
		t.Fatalf("the subscription is not found by its own path: %d", i)
	}

	info := sub.info("a@test.local")
	if info.Address != srv.URL || info.Writable || !info.SupportsEvent() || info.SupportsTodo() {
		t.Fatalf("rail entry: %+v", info)
	}
	if err := subs.refresh(srv.URL); err != nil {
		t.Fatal(err)
	}
	info.Visible = false
	if got := subscriptionObjects([]CalendarInfo{info}, ""); len(got) != 0 {
		t.Errorf("a hidden subscription drew %d objects", len(got))
	}
	info.Visible = true
	got := subscriptionObjects([]CalendarInfo{info}, "")
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
