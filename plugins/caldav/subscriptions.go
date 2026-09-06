package alborzcaldav

import (
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
func (s Subscription) info(account string) CalendarInfo {
	return CalendarInfo{
		Collection:            dav.Collection{Path: s.path(), Name: s.Name, Color: s.Color, Address: s.URL},
		SupportedComponentSet: []string{"VEVENT"},
		Account:               account,
	}
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
	mu      sync.Mutex
	entries map[string]*subEntry
	client  *http.Client
}

var subs = &subCache{entries: map[string]*subEntry{}, client: alborz.NewRemoteClient(alborz.RoundTripTimeout)}

// events returns the feed's events as one read-only object in the
// subscription's colour, or nothing when the feed has not been fetched
// yet. The colour rides the object so the views need no colour map for
// a source that is not a calendar.
func (c *subCache) events(url, color string) (CalendarObject, bool) {
	c.mu.Lock()
	e := c.entries[url]
	c.mu.Unlock()
	if e == nil || e.cal == nil {
		return CalendarObject{}, false
	}
	return CalendarObject{
		CalendarObject: &caldav.CalendarObject{Path: url, Data: e.cal},
		Color:          color,
		ReadOnly:       true,
	}, true
}

// count is how many events the fetched feed holds, 0 before a fetch.
// name is what the feed calls itself, "" when it does not say.
func (c *subCache) name(url string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[url]; e != nil && e.cal != nil {
		if p := e.cal.Props.Get(feedNameProp); p != nil {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}

func (c *subCache) count(url string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[url]; e != nil && e.cal != nil {
		return len(e.cal.Events())
	}
	return 0
}

// fresh reports whether the feed has a copy fetched within the window.
func (c *subCache) fresh(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[url]
	return e != nil && e.cal != nil && time.Since(e.fetched) < subRefreshAfter
}

// refresh fetches the feed and caches it. It honours the ETag so a feed
// that has not changed costs one conditional GET and no parse.
func (c *subCache) refresh(url string) error {
	c.mu.Lock()
	e := c.entries[url]
	if e == nil {
		e = &subEntry{}
		c.entries[url] = e
	}
	if e.fetching {
		c.mu.Unlock()
		return nil
	}
	e.fetching = true
	etag := e.etag
	c.mu.Unlock()

	cal, newETag, notModified, err := c.get(url, etag)

	c.mu.Lock()
	defer c.mu.Unlock()
	e.fetching = false
	if err != nil {
		return err
	}
	if notModified {
		e.fetched = time.Now()
		return nil
	}
	e.cal = cal
	e.etag = newETag
	e.fetched = time.Now()
	return nil
}

func (c *subCache) get(url, etag string) (*ical.Calendar, string, bool, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client.Do(req)
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
func subscriptionObjects(infos []CalendarInfo, scope string) []CalendarObject {
	var out []CalendarObject
	for _, info := range infos {
		if info.Address == "" || !info.Visible || (scope != "" && info.Account != scope) {
			continue
		}
		if !subs.fresh(info.Address) {
			go subs.refresh(info.Address)
		}
		if obj, ok := subs.events(info.Address, info.Color); ok {
			out = append(out, obj)
		}
	}
	return out
}
