package alborzcaldav

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/plugins/dav"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// A subscription is a calendar the reader follows by URL rather than
// through a CalDAV account: any public .ics address. It is read-only
// and provider-neutral, stored per account beside the reader's other
// calendar choices, and stands in the rail as a calendar of its own.
type Subscription struct {
	URL   string
	Name  string
	Color string
}

const (
	// subPrefix roots the pseudo-path a subscription is addressed by in
	// the rail, the visibility setting and its own page: a feed URL
	// cannot serve, since collection paths are canonicalised and would
	// lose its host.
	subPrefix = "/subscriptions/"
	// subRefreshAfter is how old a feed may be before the next calendar
	// view fetches it again behind the page.
	subRefreshAfter = time.Hour
	// subMaxSize bounds what a feed may hand back; a year of holidays
	// is a few tens of kilobytes.
	subMaxSize = 4 << 20

	// feedNameProp is the calendar's own name, a de facto property every
	// publisher writes.
	feedNameProp = "X-WR-CALNAME"
)

// path is the subscription's stable pseudo-path, derived from its URL.
func (s Subscription) path() string {
	sum := sha1.Sum([]byte(s.URL))
	return subPrefix + hex.EncodeToString(sum[:6]) + "/"
}

// info is the subscription as the rail and the collection page see it:
// a read-only calendar of events whose Address names the feed.
func (s Subscription) info(account string) dav.Collection {
	return dav.Collection{Path: s.path(), Name: s.Name, Color: s.Color, Address: s.URL,
		Components: []string{"VEVENT"}, Account: account}
}

func isSubscription(path string) bool { return strings.HasPrefix(path, subPrefix) }

// subscriptionAt finds the subscription a pseudo-path names, or -1.
func subscriptionAt(list []Subscription, path string) int {
	for i, s := range list {
		if s.path() == path {
			return i
		}
	}
	return -1
}

// normalizeFeedURL accepts what people paste: a webcal:// link, a
// scheme-less host, a plain https address, or the page link a service
// hands out where its feed lives elsewhere.
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

// Google's "add this calendar" link is the one page link people paste
// that carries no feed at all: it opens the web calendar, signed in,
// with the calendar id in its cid query. The public feed of that id is
// at a fixed address, so the id is enough.
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
	// Spelled as Google's own "public address" spells it, with the @
	// escaped, so the two ways of pasting one calendar meet as one.
	return fmt.Sprintf(googleFeedFormat, url.QueryEscape(string(id))), true
}

// feedHost names a feed by where it lives, for a feed that does not
// name itself.
func feedHost(feed string) string {
	u, err := url.Parse(feed)
	if err != nil || u.Host == "" {
		return feed
	}
	return u.Host
}

// errNotFeed is an address that answered with a web page: a link to a
// calendar's site rather than to the calendar.
var errNotFeed = errors.New("caldav: the address answered with a web page, not a calendar")

type subEntry struct {
	cal     *ical.Calendar
	fetched time.Time
	etag    string
	// fetching guards against two page loads refreshing the same feed
	// at once; it is not an error state, only a flag.
	fetching bool
}

type subCache struct {
	// keepFor is how old a feed may be before the next page fetches it
	// again behind itself; none keeps nothing, and a page fetches the
	// feeds it shows.
	keepFor time.Duration
	mu      sync.Mutex
	entries map[string]*subEntry
}

func newSubCache(keepFor time.Duration) *subCache {
	return &subCache{keepFor: keepFor, entries: map[string]*subEntry{}}
}

// calendar is the feed as held, nil before the first fetch lands; with
// nothing kept, it is fetched now.
func (c *subCache) calendar(client *http.Client, url string) *ical.Calendar {
	if c.keepFor == 0 {
		cal, _ := c.refresh(client, url)
		return cal
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[url]; e != nil {
		return e.cal
	}
	return nil
}

// events returns the feed's events as one read-only object in the
// subscription's colour, or nothing when the feed has not been fetched
// yet. The colour rides the object so the views need no colour map for
// a source that is not a calendar.
func (c *subCache) events(client *http.Client, url, color string) (CalendarObject, bool) {
	cal := c.calendar(client, url)
	if cal == nil {
		return CalendarObject{}, false
	}
	return CalendarObject{
		CalendarObject: &caldav.CalendarObject{Path: url, Data: cal},
		Color:          color,
		ReadOnly:       true,
	}, true
}

// feedName is what the feed calls itself, "" when it does not say.
func feedName(cal *ical.Calendar) string {
	if p := cal.Props.Get(feedNameProp); p != nil {
		return strings.TrimSpace(p.Value)
	}
	return ""
}

// count is how many events the fetched feed holds, 0 before a fetch.
func (c *subCache) count(client *http.Client, url string) int {
	if cal := c.calendar(client, url); cal != nil {
		return len(cal.Events())
	}
	return 0
}

// fresh reports whether the feed has a copy fetched within the window.
func (c *subCache) fresh(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[url]
	return e != nil && e.cal != nil && time.Since(e.fetched) < c.keepFor
}

// renew fetches a stale feed behind the page; with nothing kept there
// is nothing to renew.
func (c *subCache) renew(client *http.Client, url string) {
	if c.keepFor > 0 && !c.fresh(url) {
		go c.refresh(client, url)
	}
}

// refresh fetches the feed and keeps it. It honours the ETag so a feed
// that has not changed costs one conditional GET and no parse. A feed
// being fetched already answers with what is held.
func (c *subCache) refresh(client *http.Client, url string) (*ical.Calendar, error) {
	if c.keepFor == 0 {
		cal, _, _, err := c.get(client, url, "")
		return cal, err
	}
	c.mu.Lock()
	e := c.entries[url]
	if e == nil {
		e = &subEntry{}
		c.entries[url] = e
	}
	if e.fetching {
		cal := e.cal
		c.mu.Unlock()
		return cal, nil
	}
	e.fetching = true
	etag := e.etag
	c.mu.Unlock()

	cal, newETag, notModified, err := c.get(client, url, etag)

	c.mu.Lock()
	defer c.mu.Unlock()
	e.fetching = false
	if err != nil {
		return nil, err
	}
	if notModified {
		e.fetched = time.Now()
		return e.cal, nil
	}
	e.cal = cal
	e.etag = newETag
	e.fetched = time.Now()
	return cal, nil
}

func (c *subCache) get(client *http.Client, url, etag string) (*ical.Calendar, string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), alborz.RoundTripTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("the calendar at %s answered %s", url, resp.Status)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, "", false, errNotFeed
	}
	cal, err := ical.NewDecoder(io.LimitReader(resp.Body, subMaxSize)).Decode()
	if err != nil {
		return nil, "", false, fmt.Errorf("the calendar at %s is not iCalendar: %w", url, err)
	}
	return cal, resp.Header.Get("ETag"), false, nil
}

// subscriptionObjects gathers the events of the visible subscriptions
// among the rail's calendars, fetching a stale feed behind the page. A
// feed never fetched contributes nothing this time and its events
// appear once the refresh lands.
func (p *plugin) subscriptionObjects(client *http.Client, infos []dav.Collection, scope string) []CalendarObject {
	var out []CalendarObject
	for _, info := range infos {
		if info.Address == "" || !info.Shown || (scope != "" && info.Account != scope) {
			continue
		}
		p.subs.renew(client, info.Address)
		if obj, ok := p.subs.events(client, info.Address, info.Color); ok {
			// The link carries the account, as every object's does, so
			// the event page asks that account's subscriptions and no
			// other's.
			obj.Account = info.Account
			out = append(out, obj)
		}
	}
	return out
}
