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
	// DefaultPoll is how often a collection is asked for its ctag when
	// the service names no interval of its own. There is no push in
	// CalDAV or CardDAV, so this is how soon a change made elsewhere
	// shows up; the reader never waits on it, the loop asks ahead.
	DefaultPoll = 2 * time.Minute

	// claimRetry is how long a forced refresh waits before asking again
	// for a collection the loop is busy with.
	claimRetry = 50 * time.Millisecond

	// refreshTick is the loop's pace. An entry that would go stale
	// before the next tick is renewed now, so a reader meets a stale
	// entry only when the server was slow to answer the loop.
	refreshTick = 10 * time.Second

	// refreshBudget is what one account's background pass may spend,
	// ctag checks and replays together.
	refreshBudget = 30 * time.Second

	// ctagTimeout bounds the revalidation a stale read waits on. It is
	// one PROPFIND for one property; a server that cannot answer it in
	// this long is answered by going to the source instead.
	ctagTimeout = 5 * time.Second
	// Entries unused this long are dropped. Until then an entry is
	// served as it is; calendars and contacts change rarely, so the
	// check behind it almost always says the collection is still good.
	PruneAfter = 7 * 24 * time.Hour

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
	mu         sync.Mutex
	entries    map[string]*entry
	ctags      map[string]string // collection path -> last seen ctag
	flights    map[string]*flight
	refreshing map[string]bool // collections being revalidated
	lastActive time.Time
	// poll is the account's service interval, see DefaultPoll.
	poll time.Duration
	// dirty says the store's copy is behind what is held.
	dirty bool
}

func newUser(poll time.Duration) *user {
	return &user{
		entries:    make(map[string]*entry),
		ctags:      make(map[string]string),
		flights:    make(map[string]*flight),
		refreshing: make(map[string]bool),
		poll:       poll,
	}
}

type Cache struct {
	mu    sync.Mutex
	users map[string]*user
	store *Store // nil keeps the cache in memory only
	poll  time.Duration
	stop  chan struct{}
	wg    sync.WaitGroup
}

// New makes a cache polling collections every poll, warm from store
// when there is one. It reports how many accounts it starts with.
func New(store *Store, poll time.Duration) (*Cache, int, error) {
	c := &Cache{
		users: make(map[string]*user),
		store: store,
		poll:  poll,
		stop:  make(chan struct{}),
	}
	if store != nil {
		users, err := store.load(poll)
		if err != nil {
			return nil, 0, err
		}
		c.users = users
	}
	return c, len(c.users), nil
}

// Refresh checks every collection the account holds now, on the
// caller's time: the reader's way to force what the loop does behind.
// A collection the loop is checking at that moment is waited for, or
// the page after the click would still show what the reader asked to
// get past.
func (c *Cache) Refresh(ctx context.Context, username string) {
	u := c.user(username)
	ctx, cancel := context.WithTimeout(ctx, refreshBudget)
	defer cancel()
	colls := make(map[string][]*entry)
	u.mu.Lock()
	for _, e := range u.entries {
		if e.replay == nil {
			continue
		}
		e.fetched = time.Time{}
		coll := collectionOf(e.url.Path)
		colls[coll] = append(colls[coll], e)
	}
	u.mu.Unlock()
	for coll, entries := range colls {
		for !u.claim(coll) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(claimRetry):
			}
		}
		u.refreshCollection(ctx, coll, entries)
		u.release(coll)
	}
}

func (c *Cache) Start() {
	c.wg.Add(1)
	go c.refreshLoop()
}

func (c *Cache) Stop() {
	close(c.stop)
	c.wg.Wait()
	c.flush()
}

// flush writes every account the store is behind on.
func (c *Cache) flush() {
	if c.store == nil {
		return
	}
	c.mu.Lock()
	users := make(map[string]*user, len(c.users))
	for name, u := range c.users {
		users[name] = u
	}
	c.mu.Unlock()
	for name, u := range users {
		u.mu.Lock()
		dirty := u.dirty
		u.dirty = false
		u.mu.Unlock()
		if !dirty {
			continue
		}
		if err := c.store.save(name, u); err != nil {
			u.mu.Lock()
			u.dirty = true
			u.mu.Unlock()
		}
	}
}

func (c *Cache) user(username string) *user {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.users[username]
	if !ok {
		u = newUser(c.poll)
		c.users[username] = u
	}
	return u
}

// Forget drops everything held for a user, on disk too. Signing out
// ends the only authority the cache had to hold it.
func (c *Cache) Forget(username string) {
	c.mu.Lock()
	delete(c.users, username)
	c.mu.Unlock()
	if c.store != nil {
		c.store.remove(username)
	}
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

	// The entry is checked behind the pages through the client of
	// whoever read it last: one loaded from disk has none until then,
	// and one read by a session since expired has one that cannot
	// sign the request any more.
	u.mu.Lock()
	if e, ok := u.entries[key]; ok {
		e.replay = t.replay
	}
	u.mu.Unlock()

	if e := u.get(key); e != nil {
		// Served as it is, fresh or stale: the click never waits on the
		// server. A stale collection is checked behind the page, one
		// small PROPFIND for its ctag, and replayed only when it moved,
		// so a change from elsewhere shows one visit late at most. The
		// loop asks ahead of staleness, so this is the exception.
		if time.Since(e.fetched) > u.poll {
			u.refreshBehind(collectionOf(req.URL.Path))
		}
		return e.response(req), nil
	}

	// A collection with a ctag on record is renewed by one PROPFIND when
	// it goes stale; one without is fetched all over again. The ctag is
	// asked for before the read rather than after, so a write landing
	// between the two leaves a ctag the server no longer answers with,
	// and the next stale read fetches over it instead of trusting it.
	coll := collectionOf(req.URL.Path)
	ctag := ""
	if u.ctagOf(coll) == "" {
		ctx, cancel := context.WithTimeout(req.Context(), ctagTimeout)
		ctag = fetchCtag(ctx, t.replay, req.URL, coll)
		cancel()
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
	if ctag != "" && u.ctags[coll] == "" {
		u.ctags[coll] = ctag
	}
	u.dirty = true
	u.mu.Unlock()
	return e.response(req), nil
}

// ctagOf is the collection's last recorded ctag, "" when none is.
func (u *user) ctagOf(coll string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ctags[coll]
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
	u.dirty = true
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

	ticker := time.NewTicker(refreshTick)
	defer ticker.Stop()

	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.refresh()
			c.flush()
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
	for name, u := range users {
		u.mu.Lock()
		idle := now.Sub(u.lastActive)
		u.mu.Unlock()
		if idle > ForgetAfter {
			c.Forget(name)
			continue
		}
		// A budget per account: one slow server must not spend the time
		// every other account's refresh needed.
		ctx, cancel := context.WithTimeout(context.Background(), refreshBudget)
		u.refresh(ctx)
		cancel()
	}
}

// refresh renews the user's entries that are stale or about to be, per
// collection, and prunes what nobody has read for a long time. An
// entry with no client yet was loaded from disk and nobody has read
// it since; it waits for its reader.
func (u *user) refresh(ctx context.Context) {
	now := time.Now()
	due := make(map[string][]*entry)
	u.mu.Lock()
	for key, e := range u.entries {
		if now.Sub(e.lastUse) > PruneAfter {
			delete(u.entries, key)
			continue
		}
		if now.Sub(e.fetched) > u.poll-refreshTick && e.replay != nil {
			coll := collectionOf(e.url.Path)
			due[coll] = append(due[coll], e)
		}
	}
	u.mu.Unlock()

	for coll, entries := range due {
		if u.claim(coll) {
			u.refreshCollection(ctx, coll, entries)
			u.release(coll)
		}
	}
}

// refreshBehind renews one collection's stale entries off the request
// that found them stale, unless a renewal is already under way.
func (u *user) refreshBehind(coll string) {
	if !u.claim(coll) {
		return
	}
	now := time.Now()
	var stale []*entry
	u.mu.Lock()
	for _, e := range u.entries {
		if collectionOf(e.url.Path) == coll && now.Sub(e.fetched) > u.poll && e.replay != nil {
			stale = append(stale, e)
		}
	}
	u.mu.Unlock()
	go func() {
		defer u.release(coll)
		ctx, cancel := context.WithTimeout(context.Background(), refreshBudget)
		defer cancel()
		u.refreshCollection(ctx, coll, stale)
	}()
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
	known := u.ctagOf(coll)

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
		u.dirty = true
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
		u.dirty = true
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
	u.dirty = true
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
