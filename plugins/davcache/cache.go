// Package davcache caches CalDAV and CardDAV traffic at the HTTP transport
// level. Read responses are served from memory however old they are, and a
// stale one is revalidated against the collection's ctag before it is
// served, so a change another instance made is seen; writes evict
// by URL prefix. The plugins above stay unaware of it.
package davcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"git.mehdix.org/alborz"
)

const (
	// SoftTTL is the age past which an entry is checked before it is
	// used: the reader waits on one ctag PROPFIND for the collection,
	// which renews every stale entry of it at once. The refresh loop
	// does the same ahead of time while the user is active.
	SoftTTL = 2 * time.Minute

	// ctagTimeout bounds the revalidation a stale read waits on. It is
	// one PROPFIND for one property; a server that cannot answer it in
	// this long is answered by going to the source instead.
	ctagTimeout = 5 * time.Second
	// Entries unused this long are dropped. Until then an entry within
	// SoftTTL is served instantly and an older one costs the ctag check
	// above; calendars and contacts change rarely, so that check almost
	// always says the whole collection is still good.
	PruneAfter = 7 * 24 * time.Hour

	ActiveRefreshRate = 2 * time.Minute
	IdleRefreshRate   = 10 * time.Minute

	// ForgetAfter drops a user nobody has been seen as. It matches the
	// life of the remembered-login cookie, which is the longest a
	// browser can come back and still be signed in without typing a
	// password: past it there is nothing left to keep warm for.
	//
	// Until then an idle user keeps being refreshed. An instance that
	// runs all year is idle most of the time, and a cache that stops
	// warming after a few quiet minutes is cold every morning, which is
	// the opposite of what it is for. Signing out drops the user at
	// once - see Forget.
	ForgetAfter = 30 * 24 * time.Hour
)

// entry is one cached read response plus what is needed to replay it.
type entry struct {
	status  int
	header  http.Header
	body    []byte
	fetched time.Time
	lastUse time.Time

	method  string
	url     *url.URL
	depth   string
	reqBody []byte
	replay  *http.Client
}

func (e *entry) response(req *http.Request) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", e.status, http.StatusText(e.status)),
		StatusCode:    e.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        e.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

type user struct {
	mu          sync.Mutex
	entries     map[string]*entry
	ctags       map[string]string // collection path -> last seen ctag
	flights     map[string]*flight
	refreshing  map[string]bool // collections being revalidated
	lastActive  time.Time
	lastRefresh time.Time // only touched by the refresh loop
}

type Cache struct {
	mu    sync.Mutex
	users map[string]*user
	stop  chan struct{}
	wg    sync.WaitGroup
}

func New() *Cache {
	return &Cache{
		users: make(map[string]*user),
		stop:  make(chan struct{}),
	}
}

func (c *Cache) Start() {
	c.wg.Add(1)
	go c.refreshLoop()
}

func (c *Cache) Stop() {
	close(c.stop)
	c.wg.Wait()
}

func (c *Cache) user(username string) *user {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.users[username]
	if !ok {
		u = &user{
			entries:    make(map[string]*entry),
			ctags:      make(map[string]string),
			flights:    make(map[string]*flight),
			refreshing: make(map[string]bool),
		}
		c.users[username] = u
	}
	return u
}

// Forget drops everything held for a user. Signing out ends the only
// authority the cache had to hold it, and the entries carry the client
// that fetched them.
func (c *Cache) Forget(username string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.users, username)
}

// Transport wraps next with the cache for one user. The jar authenticates
// background replays the same way foreground requests are.
func (c *Cache) Transport(username string, next http.RoundTripper) http.RoundTripper {
	return &transport{
		cache:    c,
		username: username,
		next:     next,
		replay: &http.Client{
			Transport: next,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type transport struct {
	cache    *Cache
	username string
	next     http.RoundTripper
	replay   *http.Client
}

func cacheable(method string) bool {
	return method == "PROPFIND" || method == "REPORT" || method == http.MethodGet
}

// collectionOf maps a request path to the collection it reads: queries
// address the collection itself, object reads its parent.
func collectionOf(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return path.Dir(p) + "/"
}

// parentOf returns the collection holding this one, empty at the root.
func parentOf(collection string) string {
	trimmed := strings.TrimSuffix(collection, "/")
	if trimmed == "" {
		return ""
	}
	parent := path.Dir(trimmed)
	if parent == "/" || parent == "." {
		return ""
	}
	return parent + "/"
}

// The separator cannot appear in URL paths, which may contain spaces.
func cacheKey(method, urlPath, depth string, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\x00" + urlPath + "\x00" + depth + "\x00" + hex.EncodeToString(sum[:8])
}

func keyPath(key string) string {
	parts := strings.SplitN(key, "\x00", 3)
	return parts[1]
}

func requestBody(req *http.Request) ([]byte, error) {
	if req.GetBody == nil {
		return nil, nil
	}
	r, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	defer func() { alborz.AddTiming(req.Context(), "dav", start) }()

	u := t.cache.user(t.username)

	u.mu.Lock()
	u.lastActive = time.Now()
	u.mu.Unlock()

	if !cacheable(req.Method) {
		resp, err := t.next.RoundTrip(req)
		if err == nil && resp.StatusCode < 300 {
			collection := collectionOf(req.URL.Path)
			u.evict(collection)
			// Creating or removing a collection changes what its parent
			// lists, and that listing is how the sidebar is built.
			if parent := parentOf(collection); parent != "" {
				u.evict(parent)
			}
		}
		return resp, err
	}

	body, err := requestBody(req)
	if err != nil {
		return nil, err
	}
	key := cacheKey(req.Method, req.URL.Path, req.Header.Get("Depth"), body)

	// Singleflight: concurrent misses on one key fetch once.
	fl := u.board(key)
	fl.Lock()
	defer u.land(key, fl)

	if e := u.get(key); e != nil {
		// A fresh entry is served as it is. A stale one is checked
		// before it is served: another instance of alborz, on another
		// machine, may have written to this collection, and this one
		// hears about it only by asking. The ask is one small PROPFIND
		// for the collection's ctag, and an unchanged ctag renews every
		// stale entry it has - so the cost is one round trip per
		// collection per SoftTTL, not one per read.
		//
		// It used to serve the stale copy and revalidate behind, which
		// spent the click but left every reader one visit behind the
		// server, for ever, whenever a second instance was writing.
		if time.Since(e.fetched) > SoftTTL {
			if u.revalidate(req.Context(), collectionOf(req.URL.Path), e) {
				return e.response(req), nil
			}
			// The collection moved on: this entry and its neighbours are
			// gone, and the read below fetches what is there now.
		} else {
			return e.response(req), nil
		}
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		return resp, nil
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	e := &entry{
		status:  resp.StatusCode,
		header:  resp.Header,
		body:    respBody,
		fetched: time.Now(),
		lastUse: time.Now(),
		method:  req.Method,
		url:     req.URL,
		depth:   req.Header.Get("Depth"),
		reqBody: body,
		replay:  t.replay,
	}
	u.mu.Lock()
	u.entries[key] = e
	u.mu.Unlock()
	return e.response(req), nil
}

// flight serialises the requests for one key; waiting counts how many
// hold a reference, so the flight is dropped once the last one lands
// rather than kept for every key ever asked for.
type flight struct {
	sync.Mutex
	waiting int
}

func (u *user) board(key string) *flight {
	u.mu.Lock()
	defer u.mu.Unlock()
	fl, ok := u.flights[key]
	if !ok {
		fl = &flight{}
		u.flights[key] = fl
	}
	fl.waiting++
	return fl
}

func (u *user) land(key string, fl *flight) {
	fl.Unlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	fl.waiting--
	if fl.waiting == 0 {
		delete(u.flights, key)
	}
}

// get returns a snapshot so a concurrent replay cannot rewrite the entry
// under a reader.
func (u *user) get(key string) *entry {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.entries[key]
	if !ok {
		return nil
	}
	e.lastUse = time.Now()
	snap := *e
	return &snap
}

// evict drops the collection's entries and ctag after a write to it.
func (u *user) evict(collection string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for key := range u.entries {
		if strings.HasPrefix(keyPath(key), collection) {
			delete(u.entries, key)
		}
	}
	delete(u.ctags, collection)
}

func (c *Cache) refreshLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.refresh()
		}
	}
}

func (c *Cache) refresh() {
	c.mu.Lock()
	users := make(map[string]*user, len(c.users))
	for name, u := range c.users {
		users[name] = u
	}
	c.mu.Unlock()

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for name, u := range users {
		u.mu.Lock()
		idle := now.Sub(u.lastActive)
		u.mu.Unlock()
		if idle > ForgetAfter {
			c.Forget(name)
			continue
		}
		rate := IdleRefreshRate
		if idle < time.Minute {
			rate = ActiveRefreshRate
		}
		if now.Sub(u.lastRefresh) < rate {
			continue
		}
		u.lastRefresh = now
		u.refresh(ctx)
	}
}

// refresh revalidates the user's stale entries per collection and prunes
// what nobody has read for a long time.
func (u *user) refresh(ctx context.Context) {
	now := time.Now()
	stale := make(map[string][]*entry)
	u.mu.Lock()
	for key, e := range u.entries {
		if now.Sub(e.lastUse) > PruneAfter {
			delete(u.entries, key)
			continue
		}
		if now.Sub(e.fetched) > SoftTTL {
			coll := collectionOf(e.url.Path)
			stale[coll] = append(stale[coll], e)
		}
	}
	u.mu.Unlock()

	for coll, entries := range stale {
		if u.claim(coll) {
			u.refreshCollection(ctx, coll, entries)
			u.release(coll)
		}
	}
}

// revalidate asks the server whether the collection has changed since
// the entries were taken. It reports true when nothing has: the caller
// may serve what it holds, and every stale entry of that collection is
// renewed at once. When the ctag has moved - or cannot be had, which is
// the same answer for our purposes - the collection is dropped and the
// caller fetches afresh.
func (u *user) revalidate(ctx context.Context, coll string, sample *entry) bool {
	u.mu.Lock()
	known := u.ctags[coll]
	u.mu.Unlock()

	// A server that does not report a ctag leaves nothing to compare, so
	// the entry is not renewable and the read goes through.
	if known == "" {
		u.evict(coll)
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, ctagTimeout)
	defer cancel()
	if ctag := fetchCtag(ctx, sample.replay, sample.url, coll); ctag != "" && ctag == known {
		now := time.Now()
		u.mu.Lock()
		for _, e := range u.entries {
			if collectionOf(e.url.Path) == coll {
				e.fetched = now
			}
		}
		u.mu.Unlock()
		return true
	}
	u.evict(coll)
	return false
}

// claim marks the collection as being revalidated; false means someone
// else already is.
func (u *user) claim(coll string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.refreshing[coll] {
		return false
	}
	u.refreshing[coll] = true
	return true
}

func (u *user) release(coll string) {
	u.mu.Lock()
	delete(u.refreshing, coll)
	u.mu.Unlock()
}

// refreshCollection revalidates the collection's stale entries: an
// unchanged ctag renews them all at the cost of one tiny PROPFIND,
// otherwise each request is replayed.
func (u *user) refreshCollection(ctx context.Context, coll string, entries []*entry) {
	if len(entries) == 0 {
		return
	}
	u.mu.Lock()
	known := u.ctags[coll]
	u.mu.Unlock()

	ctag := fetchCtag(ctx, entries[0].replay, entries[0].url, coll)
	if ctag != "" && ctag == known {
		now := time.Now()
		u.mu.Lock()
		for _, e := range entries {
			e.fetched = now
		}
		u.mu.Unlock()
		return
	}
	// The ctag is the claim "the cache holds this version of the
	// collection", so it is recorded only once every entry actually
	// holds it. A replay can fail quietly - the network, a 500, a short
	// read - and recording the new ctag over an entry that still holds
	// the old body pins the collection there: every later revalidation
	// compares the new ctag against itself, matches, and renews a body
	// that is never fetched again. An event added elsewhere then never
	// arrives, for the life of the process.
	replayed := true
	for _, e := range entries {
		if !u.replayEntry(ctx, e) {
			replayed = false
		}
	}
	if ctag != "" && replayed {
		u.mu.Lock()
		u.ctags[coll] = ctag
		u.mu.Unlock()
	}
}

// replayEntry re-fetches one cached request. It reports whether the
// entry now holds what the server answered: false leaves the collection
// unrecorded, so the next read revalidates rather than trusting a body
// that was not replaced.
func (u *user) replayEntry(ctx context.Context, e *entry) bool {
	req, err := http.NewRequestWithContext(ctx, e.method, e.url.String(), bytes.NewReader(e.reqBody))
	if err != nil {
		return false
	}
	if e.depth != "" {
		req.Header.Set("Depth", e.depth)
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := e.replay.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	key := cacheKey(e.method, e.url.Path, e.depth, e.reqBody)
	if resp.StatusCode == http.StatusNotFound {
		u.mu.Lock()
		delete(u.entries, key)
		u.mu.Unlock()
		// Gone is an answer, and the entry is no longer holding a stale
		// copy of anything.
		return true
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	u.mu.Lock()
	e.status = resp.StatusCode
	e.header = resp.Header
	e.body = body
	e.fetched = time.Now()
	u.mu.Unlock()
	return true
}

type ctagMultiStatus struct {
	Responses []struct {
		PropStat []struct {
			Status string `xml:"status"`
			Prop   struct {
				CTag string `xml:"http://calendarserver.org/ns/ getctag"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// fetchCtag asks the collection for its ctag; "" means the server does not
// provide one and the caller falls back to replaying. The collection is
// addressed on the same server as the sample entry.
func fetchCtag(ctx context.Context, client *http.Client, sample *url.URL, coll string) string {
	target := *sample
	target.Path, target.RawPath, target.RawQuery = coll, "", ""
	body := `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><CS:getctag/></D:prop></D:propfind>`
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target.String(), strings.NewReader(xml.Header+body))
	if err != nil {
		return ""
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return ""
	}

	var ms ctagMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return ""
	}
	for _, r := range ms.Responses {
		for _, ps := range r.PropStat {
			if strings.Contains(ps.Status, "200") && ps.Prop.CTag != "" {
				return ps.Prop.CTag
			}
		}
	}
	return ""
}
