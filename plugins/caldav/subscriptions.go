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

// Subscription is a read-only calendar followed by its .ics address.
type Subscription struct {
	URL   string
	Name  string
	Color string
}

const (
	// A feed URL cannot be the path: collection paths are canonicalised and would lose its host.
	subPrefix       = "/subscriptions/"
	subRefreshAfter = time.Hour
	// A year of holidays is a few tens of kilobytes.
	subMaxSize = 4 << 20

	feedNameProp = "X-WR-CALNAME"
)

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

type subEntry struct {
	cal     *ical.Calendar
	fetched time.Time
	etag    string
	// fetching keeps two page loads from refreshing one feed at once.
	fetching bool
}

type subCache struct {
	// keepFor is how old a feed may get before a refetch; zero keeps nothing.
	keepFor time.Duration
	mu      sync.Mutex
	entries map[string]*subEntry
}

func newSubCache(keepFor time.Duration) *subCache {
	return &subCache{keepFor: keepFor, entries: map[string]*subEntry{}}
}

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

func feedName(cal *ical.Calendar) string {
	if p := cal.Props.Get(feedNameProp); p != nil {
		return strings.TrimSpace(p.Value)
	}
	return ""
}

func (c *subCache) count(client *http.Client, url string) int {
	if cal := c.calendar(client, url); cal != nil {
		return len(cal.Events())
	}
	return 0
}

func (c *subCache) fresh(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[url]
	return e != nil && e.cal != nil && time.Since(e.fetched) < c.keepFor
}

func (c *subCache) renew(client *http.Client, url string) {
	if c.keepFor > 0 && !c.fresh(url) {
		go c.refresh(client, url)
	}
}

// An unchanged feed costs one conditional GET and no parse.
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

func (p *plugin) subscriptionObjects(client *http.Client, infos []dav.Collection, scope string) []CalendarObject {
	var out []CalendarObject
	for _, info := range infos {
		if info.Address == "" || !info.Shown || (scope != "" && info.Account != scope) {
			continue
		}
		p.subs.renew(client, info.Address)
		if obj, ok := p.subs.events(client, info.Address, info.Color); ok {
			// The account rides the link, so the event page asks that account's subscriptions.
			obj.Account = info.Account
			out = append(out, obj)
		}
	}
	return out
}
