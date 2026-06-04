// Package davcache caches CalDAV and CardDAV traffic at the HTTP transport
// level. Read responses are served from memory, revalidated against the
// collection's ctag, and refreshed in the background while the user is
// active; writes evict by URL prefix. The plugins above stay unaware of it.
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
	"path"
	"strings"
	"sync"
	"time"
)

const (
	// SoftTTL is the age past which an entry is revalidated or replayed.
	SoftTTL = 2 * time.Minute
	// HardTTL is the age past which an entry is never served.
	HardTTL = 30 * time.Minute

	ActiveRefreshRate  = 2 * time.Minute
	IdleRefreshRate    = 10 * time.Minute
	SessionIdleTimeout = 5 * time.Minute
)

// entry is one cached read response plus what is needed to replay it.
type entry struct {
	status  int
	header  http.Header
	body    []byte
	fetched time.Time
	lastUse time.Time

	method  string
	url     string
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
	flights     map[string]*sync.Mutex
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
			entries: make(map[string]*entry),
			ctags:   make(map[string]string),
			flights: make(map[string]*sync.Mutex),
		}
		c.users[username] = u
	}
	return u
}

// Transport wraps next with the cache for one user. The jar authenticates
// background replays the same way foreground requests are.
func (c *Cache) Transport(username string, next http.RoundTripper, jar http.CookieJar) http.RoundTripper {
	return &transport{
		cache:    c,
		username: username,
		next:     next,
		replay: &http.Client{
			Transport: next,
			Jar:       jar,
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
	u := t.cache.user(t.username)

	u.mu.Lock()
	u.lastActive = time.Now()
	u.mu.Unlock()

	if !cacheable(req.Method) {
		resp, err := t.next.RoundTrip(req)
		if err == nil && resp.StatusCode < 300 {
			u.evict(collectionOf(req.URL.Path))
		}
		return resp, err
	}

	body, err := requestBody(req)
	if err != nil {
		return nil, err
	}
	key := cacheKey(req.Method, req.URL.Path, req.Header.Get("Depth"), body)

	// Singleflight: concurrent misses on one key fetch once.
	fl := u.flight(key)
	fl.Lock()
	defer fl.Unlock()

	if e := u.get(key); e != nil {
		return e.response(req), nil
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
		url:     req.URL.String(),
		depth:   req.Header.Get("Depth"),
		reqBody: body,
		replay:  t.replay,
	}
	u.mu.Lock()
	u.entries[key] = e
	u.mu.Unlock()
	return e.response(req), nil
}

func (u *user) flight(key string) *sync.Mutex {
	u.mu.Lock()
	defer u.mu.Unlock()
	fl, ok := u.flights[key]
	if !ok {
		fl = &sync.Mutex{}
		u.flights[key] = fl
	}
	return fl
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
	if time.Since(e.fetched) > HardTTL {
		delete(u.entries, key)
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
	users := make([]*user, 0, len(c.users))
	for _, u := range c.users {
		users = append(users, u)
	}
	c.mu.Unlock()

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, u := range users {
		u.mu.Lock()
		idle := now.Sub(u.lastActive)
		u.mu.Unlock()
		if idle > SessionIdleTimeout {
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

// refresh revalidates the user's stale entries per collection: an unchanged
// ctag renews them all at the cost of one tiny PROPFIND, otherwise each
// request is replayed.
func (u *user) refresh(ctx context.Context) {
	now := time.Now()
	stale := make(map[string][]*entry)
	u.mu.Lock()
	for key, e := range u.entries {
		if now.Sub(e.fetched) > HardTTL && now.Sub(e.lastUse) > HardTTL {
			delete(u.entries, key)
			continue
		}
		if now.Sub(e.fetched) > SoftTTL {
			coll := collectionOf(mustPath(e.url))
			stale[coll] = append(stale[coll], e)
		}
	}
	ctags := make(map[string]string, len(stale))
	for coll := range stale {
		ctags[coll] = u.ctags[coll]
	}
	u.mu.Unlock()

	for coll, entries := range stale {
		ctag := fetchCtag(ctx, entries[0].replay, entries[0].url, coll)
		if ctag != "" && ctag == ctags[coll] {
			u.mu.Lock()
			for _, e := range entries {
				e.fetched = now
			}
			u.mu.Unlock()
			continue
		}
		for _, e := range entries {
			u.replayEntry(ctx, e)
		}
		if ctag != "" {
			u.mu.Lock()
			u.ctags[coll] = ctag
			u.mu.Unlock()
		}
	}
}

func (u *user) replayEntry(ctx context.Context, e *entry) {
	req, err := http.NewRequestWithContext(ctx, e.method, e.url, bytes.NewReader(e.reqBody))
	if err != nil {
		return
	}
	if e.depth != "" {
		req.Header.Set("Depth", e.depth)
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")

	resp, err := e.replay.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	key := cacheKey(e.method, mustPath(e.url), e.depth, e.reqBody)
	if resp.StatusCode == http.StatusNotFound {
		u.mu.Lock()
		delete(u.entries, key)
		u.mu.Unlock()
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	u.mu.Lock()
	e.status = resp.StatusCode
	e.header = resp.Header
	e.body = body
	e.fetched = time.Now()
	u.mu.Unlock()
}

func mustPath(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j:]
		}
		return "/"
	}
	return rawURL
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
// provide one and the caller falls back to replaying.
func fetchCtag(ctx context.Context, client *http.Client, sampleURL, coll string) string {
	base := sampleURL[:len(sampleURL)-len(mustPath(sampleURL))]
	body := `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><CS:getctag/></D:prop></D:propfind>`
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", base+coll, strings.NewReader(xml.Header+body))
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
