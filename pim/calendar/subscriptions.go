package calendar

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/pim/dav"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
)

// Subscription is a calendar followed by its .ics address. The account's settings list them, so every
// alborz it signs in to follows the same, named and coloured alike; Changed dates the name and colour.
type Subscription struct {
	URL     string
	Name    string
	Color   string
	Changed time.Time `json:",omitzero"`
}

const (
	// A year of holidays is a few tens of kilobytes.
	subMaxSize = 4 << 20

	feedNameProp = "X-WR-CALNAME"

	// feedEvery is for a feed that names no interval: the hour it was before feeds named their own.
	feedEvery = time.Hour
	// feedFloor spares the publisher and the host's background share: a feed is read, not edited here.
	feedFloor = 15 * time.Minute
	// feedCeiling shows a correction to a feed that asks for a week, as holiday feeds do, the next day.
	feedCeiling = 24 * time.Hour
	// feedTick is how late a due feed may be fetched, well under feedFloor.
	feedTick = time.Minute
)

func LoadSubscriptions(ctx context.Context, settings alborz.Store) ([]Subscription, error) {
	var subs []Subscription
	if err := settings.Get(ctx, subscriptionsKey, &subs); err != nil && err != alborz.ErrNoStoreEntry {
		return nil, err
	}
	return subs, nil
}

func NormalizeFeedURL(raw string) string {
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

func FeedHost(feed string) string {
	u, err := url.Parse(feed)
	if err != nil || u.Host == "" {
		return feed
	}
	return u.Host
}

var ErrNotFeed = errors.New("caldav: the address answered with a web page, not a calendar")

// feeds fills each subscribed calendar kept here from its feed, on the feed's own clock.
type feeds struct {
	client *http.Client
	log    *log.Logger
	store  *store.Store

	// Both are by feed address: accounts following one feed share its fetch.
	mu       sync.Mutex
	fetching map[string]bool
	failed   map[string]time.Time
}

func openFeeds(srv *alborz.Server, st *store.Store) *feeds {
	f := &feeds{client: srv.Outgoing.Remote(alborz.RoundTripTimeout), log: srv.Logger(), store: st,
		fetching: map[string]bool{}, failed: map[string]time.Time{}}
	srv.Schedule.Add(feedTick, f.round)
	return f
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

// refresh fetches the feed once into every calendar filled from it; an unchanged one costs a conditional
// GET, a failed one changes nothing.
func (f *feeds) refresh(ctx context.Context, url string) error {
	f.mu.Lock()
	if f.fetching[url] {
		f.mu.Unlock()
		return nil
	}
	f.fetching[url] = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.fetching, url)
		f.mu.Unlock()
	}()

	fed, err := f.store.Feeds()
	if err != nil {
		return err
	}
	fed = slices.DeleteFunc(fed, func(c store.Collection) bool { return c.Feed.URL != url })
	if len(fed) == 0 {
		return nil
	}
	// A condition holds only for a copy every calendar has.
	was := *fed[0].Feed
	for _, c := range fed[1:] {
		if c.Feed.ETag != was.ETag || c.Feed.LastModified != was.LastModified {
			was.ETag, was.LastModified = "", ""
		}
	}
	got, cal, err := fetchFeed(ctx, f.client, was)
	var objects []store.Object
	if err == nil && cal != nil {
		objects, err = f.objects(url, cal)
	}
	for _, c := range fed {
		if err == nil {
			err = f.store.Fill(c.Ref(), got, objects)
		}
	}
	if err != nil {
		f.mu.Lock()
		f.failed[url] = time.Now()
		f.mu.Unlock()
	}
	return err
}

// objects are a fetched feed's events one object per UID, named as an import names them.
func (f *feeds) objects(url string, cal *ical.Calendar) ([]store.Object, error) {
	split, err := objectsByUID(cal)
	if err != nil {
		return nil, err
	}
	objects := make([]store.Object, 0, len(split))
	for uid, obj := range split {
		if len(obj.Events()) == 0 {
			continue
		}
		var buf bytes.Buffer
		if err := ical.NewEncoder(&buf).Encode(obj); err != nil {
			// One malformed event is left out rather than holding back the rest of the feed.
			f.log.Printf("calendar: feed %s: event %s: %v", url, uid, err)
			continue
		}
		objects = append(objects, store.Object{Name: dav.SafeObjectName(uid) + ".ics", UID: uid, Data: buf.Bytes()})
	}
	return objects, nil
}

// stamp gives a component the DTSTAMP a feed or a file may leave out and a stored object must carry (RFC 5545
// 3.6.1): its own LAST-MODIFIED or CREATED, else the epoch; never the fetch time, which would change every
// object's ETag.
func stamp(ev *ical.Component) {
	if ev.Props.Get(ical.PropDateTimeStamp) != nil {
		return
	}
	for _, name := range []string{ical.PropLastModified, ical.PropCreated} {
		if t, err := ev.Props.DateTime(name, time.UTC); err == nil && !t.IsZero() {
			ev.Props.SetDateTime(ical.PropDateTimeStamp, t.UTC())
			return
		}
	}
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Unix(0, 0).UTC())
}

// round fetches every feed whose interval has passed: a phone reads its calendars without alborz seeing.
func (f *feeds) round(ctx context.Context, now time.Time) {
	fed, err := f.store.Feeds()
	if err != nil {
		f.log.Printf("calendar: feeds: %v", err)
		return
	}
	due := map[string]bool{}
	f.mu.Lock()
	for _, c := range fed {
		if now.Sub(c.Feed.Fetched) >= c.Feed.Every && now.Sub(f.failed[c.Feed.URL]) >= feedFloor {
			due[c.Feed.URL] = true
		}
	}
	f.mu.Unlock()
	for url := range due {
		if err := f.refresh(alborz.Background(ctx), url); err != nil {
			f.log.Printf("calendar: feed %s: %v", url, err)
		}
	}
}

func FeedName(cal *ical.Calendar) string {
	if p := cal.Props.Get(feedNameProp); p != nil {
		return strings.TrimSpace(p.Value)
	}
	return ""
}

// fetchFeed asks for the feed unless it is unchanged since was; the calendar is nil when it is.
func fetchFeed(ctx context.Context, client *http.Client, was store.Feed) (store.Feed, *ical.Calendar, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, was.URL, nil)
	if err != nil {
		return was, nil, err
	}
	if was.ETag != "" {
		req.Header.Set("If-None-Match", was.ETag)
	}
	if was.LastModified != "" {
		req.Header.Set("If-Modified-Since", was.LastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return was, nil, err
	}
	defer resp.Body.Close()
	now := time.Now()
	if resp.StatusCode == http.StatusNotModified {
		was.Fetched = now
		return was, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return was, nil, fmt.Errorf("the calendar at %s answered %s", was.URL, resp.Status)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return was, nil, ErrNotFeed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, subMaxSize))
	if err != nil {
		return was, nil, err
	}
	cal, err := ical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return was, nil, fmt.Errorf("the calendar at %s is not iCalendar: %w", was.URL, err)
	}
	return store.Feed{URL: was.URL, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		Fetched: now, Every: refreshEvery(cal)}, cal, nil
}

// reconcile makes the account's fed calendars here agree with its list of subscriptions, which travels
// between alborz instances while the calendars do not: a calendar deleted here leaves the list, an entry
// gone from the list takes its calendar, an entry this alborz lacks is made, and the newer name and
// colour wins. It returns the calendars made, empty.
func reconcile(ctx context.Context, st *store.Store, account string, settings alborz.Store) ([]store.Collection, error) {
	subs, err := LoadSubscriptions(ctx, settings)
	if err != nil {
		return nil, err
	}
	dropped, err := st.Dropped(account)
	if err != nil {
		return nil, err
	}
	fed, err := st.Feeds()
	if err != nil {
		return nil, err
	}
	listed := len(subs)
	for _, d := range dropped {
		// An entry written after the delete is the reader subscribing again.
		subs = slices.DeleteFunc(subs, func(s Subscription) bool { return s.URL == d.URL && !s.Changed.After(d.At) })
	}
	relisted := len(subs) != listed
	held := map[string]store.Collection{}
	for _, c := range fed {
		if c.Owner == strings.ToLower(account) {
			held[c.Feed.URL] = c
		}
	}
	var made []store.Collection
	for i, sub := range subs {
		c, ok := held[sub.URL]
		delete(held, sub.URL)
		switch {
		case !ok:
			ref := st.FeedRef(account, sub.URL)
			c, err := st.Create(store.Collection{ID: ref.ID, Owner: ref.Owner, Kind: store.Calendar,
				Components: []string{ical.CompEvent}, Name: sub.Name, Color: sub.Color,
				Feed: &store.Feed{URL: sub.URL, Named: sub.Changed}})
			if errors.Is(err, store.ErrExists) {
				continue
			}
			if err != nil {
				return made, err
			}
			made = append(made, *c)
		case c.Feed.Named.After(sub.Changed):
			subs[i].Name, subs[i].Color, subs[i].Changed = c.Name, c.Color, c.Feed.Named
			relisted = true
		case sub.Changed.After(c.Feed.Named):
			err := st.Update(c.Ref(), func(c *store.Collection) {
				c.Name, c.Color, c.Feed.Named = sub.Name, sub.Color, sub.Changed
			})
			if err != nil {
				return made, err
			}
		}
	}
	if relisted {
		if err := settings.Put(ctx, subscriptionsKey, subs); err != nil {
			return made, err
		}
	}
	// What held still has, the list no longer names: unsubscribed on another alborz.
	for _, c := range held {
		if err := st.Delete(c.Ref()); err != nil {
			return made, err
		}
		dropped = append(dropped, store.Dropped{ID: c.ID})
	}
	for _, d := range dropped {
		if err := st.Told(store.Ref{Owner: account, ID: d.ID}); err != nil {
			return made, err
		}
	}
	return made, nil
}

// follows reconciles the login's calendars with its subscriptions, has fill fill each calendar made, and shows it.
func (p *Calendars) follows(ctx context.Context, login *alborz.Login, fill func(store.Collection) error) error {
	made, err := reconcile(ctx, p.feeds.store, login.Username(), login.Store())
	if err != nil {
		return err
	}
	for _, c := range made {
		if err := fill(c); err != nil {
			return err
		}
		if err := Show(ctx, login, p.DAV.PathOf(login.Username(), c)); err != nil {
			return err
		}
	}
	return nil
}

// RefreshFeeds fetches the feeds of the logins' calendars.
func (p *Calendars) RefreshFeeds(ctx context.Context, logins []*alborz.Login) error {
	var fetches sync.WaitGroup
	fed, err := p.feeds.store.Feeds()
	if err != nil {
		return err
	}
	signed := map[string]bool{}
	for _, login := range logins {
		signed[strings.ToLower(login.Username())] = true
	}
	urls := map[string]bool{}
	for _, c := range fed {
		if signed[c.Owner] {
			urls[c.Feed.URL] = true
		}
	}
	for url := range urls {
		fetches.Go(func() {
			if err := p.feeds.refresh(ctx, url); err != nil {
				p.feeds.log.Printf("calendar: feed %s: %v", url, err)
			}
		})
	}
	fetches.Wait()
	return nil
}

// SaveSubscriptions keeps the list of the account's subscriptions in its settings.
func SaveSubscriptions(ctx context.Context, settings alborz.Store, subs []Subscription) error {
	return settings.Put(ctx, subscriptionsKey, subs)
}

// Fetch reads the feed at url as a subscription does.
func (p *Calendars) Fetch(ctx context.Context, url string) (store.Feed, *ical.Calendar, error) {
	return fetchFeed(ctx, p.feeds.client, store.Feed{URL: url})
}

// Fill fills the login's calendar of the feed at url with what was fetched, making the calendar first.
func (p *Calendars) Fill(ctx context.Context, login *alborz.Login, url string, got store.Feed, cal *ical.Calendar) error {
	objects, err := p.feeds.objects(url, cal)
	if err != nil {
		return err
	}
	return p.follows(ctx, login, func(c store.Collection) error { return p.feeds.store.Fill(c.Ref(), got, objects) })
}
