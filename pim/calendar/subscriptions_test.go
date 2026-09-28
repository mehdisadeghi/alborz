package calendar

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"git.mehdix.org/alborz/pim/dav"
)

// The production client refuses loopback, so the test injects a plain one.
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

	c := newSubCache(subRefreshAfter)
	if _, err := c.refresh(srv.Client(), srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	obj, ok := c.events(srv.Client(), srv.URL, "#123456")
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
	if _, err := c.refresh(srv.Client(), srv.URL); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if hits != 2 {
		t.Fatalf("server saw %d requests, want 2", hits)
	}
	if _, ok := c.events(srv.Client(), srv.URL, "#123456"); !ok {
		t.Fatal("the feed lost its events after a not-modified refresh")
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
	p := &plugin{subs: newSubCache(subRefreshAfter)}
	if _, err := p.subs.refresh(srv.Client(), srv.URL); err != nil {
		t.Fatal(err)
	}
	info.Shown = false
	if got := p.subscriptionObjects(srv.Client(), []dav.Collection{info}, ""); len(got) != 0 {
		t.Errorf("a hidden subscription drew %d objects", len(got))
	}
	info.Shown = true
	got := p.subscriptionObjects(srv.Client(), []dav.Collection{info}, "")
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
