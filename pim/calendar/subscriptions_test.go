package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
	"github.com/labstack/echo/v4"
)

// The production client refuses loopback, so the tests inject a plain one.
func feedsAt(t *testing.T, client *http.Client) *feeds {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &feeds{client: client, log: echo.New().Logger, store: store.New(db),
		fetching: map[string]bool{}, failed: map[string]time.Time{}}
}

// shared are an account's settings as its mail server keeps them, which every alborz reads.
type shared map[string][]byte

func (s shared) Get(_ context.Context, key string, out any) error {
	raw, ok := s[key]
	if !ok {
		return alborz.ErrNoStoreEntry
	}
	return json.Unmarshal(raw, out)
}

func (s shared) Put(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	s[key] = raw
	return err
}

const holidays = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nREFRESH-INTERVAL;VALUE=DURATION:PT2H\r\n" +
	"BEGIN:VEVENT\r\nUID:h1\r\nDTSTART;VALUE=DATE:20260101\r\nSUMMARY:New Year\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:h2\r\nDTSTART;VALUE=DATE:20261225\r\nSUMMARY:Christmas\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// Two accounts following one feed cost the publisher one request per round, as one account does.
func TestFeedRefreshesOnItsOwnClockOncePerFeed(t *testing.T) {
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
		w.Write([]byte(holidays))
	}))
	defer srv.Close()
	f := feedsAt(t, srv.Client())
	accounts := []string{"a@test.local", "b@test.local"}
	for _, account := range accounts {
		list := shared{}
		if err := list.Put(context.Background(), subscriptionsKey, []Subscription{{URL: srv.URL, Name: "Holidays"}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := reconcile(context.Background(), f.store, account, list); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	f.round(context.Background(), now)
	if hits != 1 {
		t.Fatalf("two calendars never fetched cost %d requests, want one", hits)
	}
	f.round(context.Background(), now.Add(time.Hour))
	if hits != 1 {
		t.Fatalf("the feed was fetched an hour after it asked for two")
	}
	f.round(context.Background(), now.Add(3*time.Hour))
	if hits != 2 || asked.Get("If-None-Match") != `"v1"` || asked.Get("If-Modified-Since") != modified {
		t.Fatalf("the due fetch was request %d with validators %q %q, want request 2 conditional on the stored ones",
			hits, asked.Get("If-None-Match"), asked.Get("If-Modified-Since"))
	}
	for _, account := range accounts {
		if objects, err := f.store.Objects(feedCalendar(account, srv.URL)); err != nil || len(objects) != 2 {
			t.Fatalf("%s's calendar holds %d objects (%v), want the feed's two", account, len(objects), err)
		}
	}
}

const account = "a@test.local"

// twoAlborz are two alborz instances whose account follows one feed in the settings both read. The rig has
// no METADATA, so two alborz cannot share an account's settings over IMAP; here they share them directly.
func twoAlborz(t *testing.T) (first, second *feeds, list shared) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(holidays))
	}))
	t.Cleanup(srv.Close)
	list = shared{}
	if err := list.Put(context.Background(), subscriptionsKey,
		[]Subscription{{URL: srv.URL, Name: "Holidays", Color: "#123456"}}); err != nil {
		t.Fatal(err)
	}
	var instances []*feeds
	for range 2 {
		f := feedsAt(t, srv.Client())
		made, _, err := reconcile(context.Background(), f.store, account, list)
		if err != nil || len(made) != 1 {
			t.Fatalf("an alborz made %d calendars (%v), want the subscription's one", len(made), err)
		}
		if err := f.refresh(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
		instances = append(instances, f)
	}
	return instances[0], instances[1], list
}

// fedCalendar is the account's one calendar filled from a feed on this alborz, or nil.
func fedCalendar(t *testing.T, f *feeds) *store.Collection {
	t.Helper()
	fed, err := f.store.Feeds()
	if err != nil || len(fed) > 1 {
		t.Fatalf("the alborz holds %d fed calendars (%v)", len(fed), err)
	}
	if len(fed) == 0 {
		return nil
	}
	return &fed[0]
}

func TestAnotherAlborzFollowsTheSameList(t *testing.T) {
	first, second, list := twoAlborz(t)
	for _, f := range []*feeds{first, second} {
		c := fedCalendar(t, f)
		if c == nil || c.Name != "Holidays" || c.Color != "#123456" {
			t.Fatalf("an alborz keeps %+v, want Holidays in its colour", c)
		}
		if objects, err := f.store.Objects(c.Ref()); err != nil || len(objects) != 2 {
			t.Fatalf("an alborz holds %d events (%v), want the feed's two", len(objects), err)
		}
		if made, changed, err := reconcile(context.Background(), f.store, account, list); err != nil || changed || len(made) != 0 {
			t.Fatalf("a second look made %d calendars, changed %v (%v), want nothing", len(made), changed, err)
		}
	}
}

// A rename goes through the store's update whether a page or a phone sends the PROPPATCH.
func TestARenameTravels(t *testing.T) {
	first, second, list := twoAlborz(t)
	if err := first.store.Update(fedCalendar(t, first).Ref(), func(c *store.Collection) { c.Name, c.Color = "Feasts", "#654321" }); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*feeds{first, second} {
		if _, _, err := reconcile(context.Background(), f.store, account, list); err != nil {
			t.Fatal(err)
		}
		if c := fedCalendar(t, f); c.Name != "Feasts" || c.Color != "#654321" {
			t.Errorf("after the rename an alborz calls it %s in %s, want Feasts in #654321", c.Name, c.Color)
		}
	}
	subs, err := loadSubscriptions(context.Background(), list)
	if err != nil || len(subs) != 1 || subs[0].Name != "Feasts" {
		t.Errorf("the list holds %+v (%v), want Feasts", subs, err)
	}
}

// A delete goes through the store whether a page or a phone sends it; the calendar is derived, so another
// alborz drops its copy when the list no longer names it.
func TestAnUnsubscribeTravels(t *testing.T) {
	first, second, list := twoAlborz(t)
	if err := first.store.Delete(fedCalendar(t, first).Ref()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*feeds{first, second} {
		if _, _, err := reconcile(context.Background(), f.store, account, list); err != nil {
			t.Fatal(err)
		}
		if c := fedCalendar(t, f); c != nil {
			t.Errorf("after unsubscribing an alborz still holds %s", c.Name)
		}
		if dropped, err := f.store.Dropped(account); err != nil || len(dropped) != 0 {
			t.Errorf("an alborz still keeps %d deletes to tell (%v)", len(dropped), err)
		}
	}
	if subs, err := loadSubscriptions(context.Background(), list); err != nil || len(subs) != 0 {
		t.Errorf("the list still holds %+v (%v)", subs, err)
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
