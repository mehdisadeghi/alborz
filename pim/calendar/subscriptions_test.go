package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/keyring"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/internal/seal"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
)

// The production client refuses loopback, so the tests inject a plain one.
func feedsAt(t *testing.T, client *http.Client) (*feeds, *keyring.Ring) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "alborz.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := seal.ParseRoot("YLZFnivEgqo-9cIJcqU6wOS7LhhCrXtgxRvYHoQ6NmA=")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return &feeds{client: client, log: log.Default(), store: store.New(db, root, ring),
		fetching: map[string]bool{}, failed: map[string]time.Time{}}, ring
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
	f, _ := feedsAt(t, srv.Client())
	accounts := []string{"a@test.local", "b@test.local"}
	for _, account := range accounts {
		list := shared{}
		if err := list.Put(context.Background(), subscriptionsKey, []Subscription{{URL: srv.URL, Name: "Holidays"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := reconcile(context.Background(), f.store, account, list); err != nil {
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
		if objects, err := f.store.Objects(f.store.FeedRef(account, srv.URL)); err != nil || len(objects) != 2 {
			t.Fatalf("%s's calendar holds %d objects (%v), want the feed's two", account, len(objects), err)
		}
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
		if got := NormalizeFeedURL(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// An encrypted account's subscription is fetched only while a login of the account holds its key (ADR 71).
func TestAFeedWaitsForItsEncryptedOwner(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(holidays))
	}))
	defer srv.Close()
	f, ring := feedsAt(t, srv.Client())
	const account = "a@test.local"
	own := bytes.Repeat([]byte{7}, seal.KeySize)
	release, err := ring.Hold(ring.Name(account), own, true)
	if err != nil {
		t.Fatal(err)
	}
	list := shared{}
	if err := list.Put(context.Background(), subscriptionsKey, []Subscription{{URL: srv.URL, Name: "Holidays"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(context.Background(), f.store, account, list); err != nil {
		t.Fatal(err)
	}
	release()

	now := time.Now()
	f.round(context.Background(), now)
	if hits != 0 {
		t.Fatalf("with no login of its owner the feed was fetched %d times", hits)
	}
	if _, err := ring.Hold(ring.Name(account), own, true); err != nil {
		t.Fatal(err)
	}
	f.round(context.Background(), now)
	if objects, err := f.store.Objects(f.store.FeedRef(account, srv.URL)); hits != 1 || err != nil || len(objects) != 2 {
		t.Fatalf("with its owner back: %d fetches, %d objects (%v), want one and the feed's two", hits, len(objects), err)
	}
}
