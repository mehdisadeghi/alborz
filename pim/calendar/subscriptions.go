package calendar

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/internal/kv"
	"git.mehdix.org/alborz/internal/kv/bolt"
	"git.mehdix.org/alborz/pim/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/fernet/fernet-go"
	"github.com/labstack/echo/v4"
)

// Subscription is a read-only calendar followed by its .ics address.
type Subscription struct {
	URL   string
	Name  string
	Color string
}

const (
	// A feed URL cannot be the path: collection paths are canonicalised and would lose its host.
	subPrefix = "/subscriptions/"
	// A year of holidays is a few tens of kilobytes.
	subMaxSize = 4 << 20

	feedNameProp = "X-WR-CALNAME"

	// feedEvery is for a feed that names no interval: the hour it was before feeds named their own.
	feedEvery = time.Hour
	// feedFloor spares the publisher and the host's background share: a feed is read, not edited here.
	feedFloor = 15 * time.Minute
	// feedCeiling shows a correction to a feed that asks for a week, as holiday feeds do, the next day.
	feedCeiling = 24 * time.Hour
	// feedForgetAfter stops refreshing a feed nobody has read for this long; its copy stays on disk.
	feedForgetAfter = 7 * 24 * time.Hour
	// feedTick is how late a due feed may be fetched, well under feedFloor.
	feedTick = time.Minute
)

func loadSubscriptions(ctx context.Context, store alborz.Store) ([]Subscription, error) {
	var subs []Subscription
	if err := store.Get(ctx, subscriptionsKey, &subs); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return subs, nil
}

func (s Subscription) path() string {
	sum := sha1.Sum([]byte(s.URL))
	return subPrefix + hex.EncodeToString(sum[:6]) + "/"
}

func (s Subscription) info(account string) dav.Collection {
	return dav.Collection{Path: s.path(), Name: s.Name, Color: s.Color, Address: s.URL,
		Components: []string{"VEVENT"}, Account: account}
}

func isSubscription(path string) bool { return strings.HasPrefix(path, subPrefix) }

func subscriptionAt(list []Subscription, path string) int {
	for i, s := range list {
		if s.path() == path {
			return i
		}
	}
	return -1
}

func normalizeFeedURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "webcal://")
	switch {
	case strings.HasPrefix(raw, "//"):
		raw = "https:" + raw
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"):
	default:
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		if feed, ok := feedBehindPage(u); ok {
			return feed
		}
	}
	return raw
}

// Google's "add this calendar" link holds no feed, only the calendar id in cid.
const (
	googleCalendarHost = "calendar.google.com"
	googleFeedFormat   = "https://calendar.google.com/calendar/ical/%s/public/basic.ics"
)

// feedBehindPage maps a service's page link to the feed it stands for.
func feedBehindPage(u *url.URL) (string, bool) {
	if u.Host != googleCalendarHost || u.Query().Get("cid") == "" {
		return "", false
	}
	cid := strings.TrimRight(u.Query().Get("cid"), "=")
	id, err := base64.RawStdEncoding.DecodeString(cid)
	if err != nil {
		return "", false
	}
	// Escaped as Google's public address is, so both pastes meet as one.
	return fmt.Sprintf(googleFeedFormat, url.QueryEscape(string(id))), true
}

func feedHost(feed string) string {
	u, err := url.Parse(feed)
	if err != nil || u.Host == "" {
		return feed
	}
	return u.Host
}

var errNotFeed = errors.New("caldav: the address answered with a web page, not a calendar")

// feeds keeps each feed a page reads and fetches it on the feed's own clock.
// Without a db nothing is kept and every read fetches.
type feeds struct {
	client *http.Client
	log    echo.Logger
	db     kv.DB
	key    *fernet.Key

	mu   sync.Mutex
	held map[string]*heldFeed
}

// feed is what is kept of one feed besides its body.
type feed struct {
	URL, ETag, LastModified string
	Fetched                 time.Time
	Every                   time.Duration
}

type heldFeed struct {
	feed
	cal          *ical.Calendar
	read, failed time.Time
	fetching     bool
}

// Keys hash the address, so the file names no feed.
const feedOwner = "calendar.feeds"

func feedID(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:16])
}

func feedMetaKey(id string) string { return feedOwner + "/meta/" + id }
func feedBodyKey(id string) string { return feedOwner + "/body/" + id }

// feedsLockTimeout is short: the CalDAV mirror, opened first, already waited for the directory.
const feedsLockTimeout = time.Second

// openFeeds keeps feeds sealed in the cache directory and refreshes them on the server's schedule.
func openFeeds(srv *alborz.Server) (*feeds, error) {
	f := &feeds{client: srv.Outgoing.Remote(alborz.RoundTripTimeout), log: srv.Logger(), held: map[string]*heldFeed{}}
	if !srv.Options.Cache || srv.Options.CacheDir == "" {
		return f, nil
	}
	if err := os.MkdirAll(srv.Options.CacheDir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(srv.Options.CacheDir, feedOwner+".db")
	db, err := bolt.Open(file, feedsLockTimeout)
	if err != nil {
		return nil, err
	}
	f.db, f.key = db, srv.Options.LoginKey
	if err := alborz.CheckKey(db, file, f.key, feedOwner+"/"); err != nil {
		db.Close()
		return nil, err
	}
	srv.Schedule.Add(feedTick, f.round)
	srv.OnClose = append(srv.OnClose, db.Close)
	return f, nil
}

// refreshEvery is the interval the feed asks for (RFC 7986, or the older X-PUBLISHED-TTL), within bounds.
func refreshEvery(cal *ical.Calendar) time.Duration {
	for _, name := range []string{"REFRESH-INTERVAL", "X-PUBLISHED-TTL"} {
		if p := cal.Props.Get(name); p != nil {
			if d, err := p.Duration(); err == nil {
				return min(max(d, feedFloor), feedCeiling)
			}
		}
	}
	return feedEvery
}

// calendar is the feed as last fetched. One with nothing kept is fetched now, once; nil if that fails.
func (f *feeds) calendar(url string) *ical.Calendar {
	if f.db == nil {
		cal, _ := f.refresh(context.Background(), url)
		return cal
	}
	f.mu.Lock()
	h := f.hold(url)
	h.read = time.Now()
	cal, cold := h.cal, h.cal == nil && h.failed.IsZero()
	f.mu.Unlock()
	if !cold {
		return cal
	}
	cal, err := f.refresh(context.Background(), url)
	if err != nil {
		f.log.Printf("calendar: feed %s: %v", url, err)
	}
	return cal
}

// hold finds the feed in memory, or what the cache directory kept of it; f.mu is held.
func (f *feeds) hold(url string) *heldFeed {
	h := f.held[url]
	if h == nil {
		// Asked for now, so the next round does not take it for unread.
		h = &heldFeed{feed: feed{URL: url}, read: time.Now()}
		if err := f.load(h); err != nil {
			f.log.Printf("calendar: feed %s: %v", url, err)
		}
		f.held[url] = h
	}
	return h
}

func (f *feeds) load(h *heldFeed) error {
	var meta, body []byte
	err := f.db.View(func(tx kv.Tx) error {
		id := feedID(h.URL)
		var err error
		if meta, err = f.open(tx.Get(feedMetaKey(id))); err != nil {
			return err
		}
		body, err = f.open(tx.Get(feedBodyKey(id)))
		return err
	})
	if err != nil || meta == nil {
		return err
	}
	var kept feed
	if err := json.Unmarshal(meta, &kept); err != nil {
		return err
	}
	cal, err := ical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return err
	}
	h.feed, h.cal = kept, cal
	return nil
}

var errUnsealed = errors.New("calendar: a kept feed does not open under the login key")

// open unseals a stored value; a missing one is nil.
func (f *feeds) open(sealed []byte) ([]byte, error) {
	if sealed == nil {
		return nil, nil
	}
	raw := fernet.VerifyAndDecrypt(sealed, 0, []*fernet.Key{f.key})
	if raw == nil {
		return nil, errUnsealed
	}
	return raw, nil
}

// refresh fetches the feed now; an unchanged one costs a conditional GET and no parse.
func (f *feeds) refresh(ctx context.Context, url string) (*ical.Calendar, error) {
	if f.db == nil {
		_, cal, _, err := fetchFeed(ctx, f.client, feed{URL: url})
		return cal, err
	}
	f.mu.Lock()
	h := f.hold(url)
	if h.fetching {
		cal := h.cal
		f.mu.Unlock()
		return cal, nil
	}
	h.fetching = true
	asked := h.feed
	if h.cal == nil {
		asked.ETag, asked.LastModified = "", ""
	}
	f.mu.Unlock()

	got, cal, body, err := fetchFeed(ctx, f.client, asked)
	if err == nil {
		err = f.keep(got, body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	h.fetching = false
	if err != nil {
		h.failed = time.Now()
		return nil, err
	}
	h.feed = got
	if cal != nil {
		h.cal = cal
	}
	return h.cal, nil
}

// keep writes what was fetched; a nil body is a feed unchanged since the one kept.
func (f *feeds) keep(got feed, body []byte) error {
	meta, err := json.Marshal(got)
	if err != nil {
		return err
	}
	return f.db.Update(func(tx kv.Tx) error {
		id := feedID(got.URL)
		if body != nil {
			if err := f.put(tx, feedBodyKey(id), body); err != nil {
				return err
			}
		}
		return f.put(tx, feedMetaKey(id), meta)
	})
}

func (f *feeds) put(tx kv.Tx, k string, v []byte) error {
	sealed, err := fernet.EncryptAndSign(v, f.key)
	if err != nil {
		return err
	}
	return tx.Put(k, sealed)
}

// round fetches the feeds read lately whose interval has passed, and stops refreshing the ones nobody reads.
func (f *feeds) round(ctx context.Context, now time.Time) {
	var due []string
	f.mu.Lock()
	for url, h := range f.held {
		switch {
		case now.Sub(h.read) > feedForgetAfter:
			delete(f.held, url)
		case !h.fetching && now.Sub(h.Fetched) >= h.Every && now.Sub(h.failed) >= feedFloor:
			due = append(due, url)
		}
	}
	f.mu.Unlock()
	for _, url := range due {
		if _, err := f.refresh(alborz.Background(ctx), url); err != nil {
			f.log.Printf("calendar: feed %s: %v", url, err)
		}
	}
}

func (f *feeds) events(url, color string) (CalendarObject, bool) {
	cal := f.calendar(url)
	if cal == nil {
		return CalendarObject{}, false
	}
	return CalendarObject{
		CalendarObject: &caldav.CalendarObject{Path: url, Data: cal},
		Color:          color,
		ReadOnly:       true,
	}, true
}

func (f *feeds) count(url string) int {
	if cal := f.calendar(url); cal != nil {
		return len(cal.Events())
	}
	return 0
}

func feedName(cal *ical.Calendar) string {
	if p := cal.Props.Get(feedNameProp); p != nil {
		return strings.TrimSpace(p.Value)
	}
	return ""
}

// fetchFeed asks for the feed unless it is unchanged since was; calendar and body are nil when it is.
func fetchFeed(ctx context.Context, client *http.Client, was feed) (feed, *ical.Calendar, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, was.URL, nil)
	if err != nil {
		return was, nil, nil, err
	}
	if was.ETag != "" {
		req.Header.Set("If-None-Match", was.ETag)
	}
	if was.LastModified != "" {
		req.Header.Set("If-Modified-Since", was.LastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return was, nil, nil, err
	}
	defer resp.Body.Close()
	now := time.Now()
	if resp.StatusCode == http.StatusNotModified {
		was.Fetched = now
		return was, nil, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return was, nil, nil, fmt.Errorf("the calendar at %s answered %s", was.URL, resp.Status)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return was, nil, nil, errNotFeed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, subMaxSize))
	if err != nil {
		return was, nil, nil, err
	}
	cal, err := ical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return was, nil, nil, fmt.Errorf("the calendar at %s is not iCalendar: %w", was.URL, err)
	}
	return feed{URL: was.URL, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		Fetched: now, Every: refreshEvery(cal)}, cal, body, nil
}

func (p *plugin) subscriptionObjects(infos []dav.Collection, scope string) []CalendarObject {
	var out []CalendarObject
	for _, info := range infos {
		if info.Address == "" || !info.Shown || (scope != "" && info.Account != scope) {
			continue
		}
		if obj, ok := p.feeds.events(info.Address, info.Color); ok {
			// The account rides the link, so the event page asks that account's subscriptions.
			obj.Account = info.Account
			out = append(out, obj)
		}
	}
	return out
}

// refresh fetches every signed-in account's feeds, then asks the DAV servers again.
func (p *plugin) refresh(ctx *alborz.Context) error {
	rc := ctx.Request().Context()
	var fetches sync.WaitGroup
	for _, login := range ctx.Logins() {
		subs, err := loadSubscriptions(rc, login.Store())
		if err != nil {
			return err
		}
		for _, sub := range subs {
			fetches.Go(func() {
				if _, err := p.feeds.refresh(rc, sub.URL); err != nil {
					ctx.Logger().Printf("calendar: feed %s: %v", sub.URL, err)
				}
			})
		}
	}
	fetches.Wait()
	return p.dav.HandleRefresh("/calendar")(ctx)
}
