package davcache

import (
	"context"
	"github.com/fernet/fernet-go"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// server stands in for a DAV server holding one collection whose path
// needs escaping, which is where a cache keyed two ways came apart.
type server struct {
	mu      sync.Mutex
	calls   map[string]int
	ctag    string
	missing bool
}

func (s *server) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls[req.Method]++
	s.mu.Unlock()
	status, body := http.StatusMultiStatus, "<report/>"
	switch {
	case req.Method == "PROPFIND" && req.Header.Get("Depth") == "0":
		body = `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">` +
			`<D:response><D:propstat><D:prop><CS:getctag>` + s.ctag + `</CS:getctag></D:prop>` +
			`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	case s.missing:
		status, body = http.StatusNotFound, ""
	}
	return &http.Response{StatusCode: status, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (s *server) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (u *user) age() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.entries {
		e.fetched = time.Now().Add(-2 * DefaultPoll)
	}
}

func TestCacheKeysOnTheDecodedPath(t *testing.T) {
	srv := &server{calls: map[string]int{}, ctag: "1"}
	c, _, _ := New(nil, DefaultPoll)
	rt := c.Transport("u", srv)
	report := func() {
		t.Helper()
		req, _ := http.NewRequest("REPORT", "https://dav.example/c/Work%20Cal/", strings.NewReader("<q/>"))
		req.Header.Set("Depth", "1")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	report()
	report()
	if n := srv.count("REPORT"); n != 1 {
		t.Fatalf("a fresh entry was fetched %d times", n)
	}
	// The miss asked for the ctag once, so the collection is renewable
	// from its first read, not only after the loop has replayed it.
	if n := srv.count("PROPFIND"); n != 1 {
		t.Fatalf("expected the ctag to be asked for on the miss, got %d PROPFINDs", n)
	}

	u := c.user("u")
	u.age()
	u.refresh(context.Background())
	if n := srv.count("REPORT"); n != 1 {
		t.Errorf("an unchanged collection was fetched again: %d fetches", n)
	}
	if n := srv.count("PROPFIND"); n != 2 {
		t.Errorf("expected one ctag check per stale pass, got %d", n)
	}

	// A changed ctag has the pass replay the entry and record the new
	// ctag with it.
	srv.ctag = "2"
	u.age()
	u.refresh(context.Background())
	if n := srv.count("REPORT"); n != 2 {
		t.Errorf("a changed collection was not fetched again: %d fetches", n)
	}
	if got := u.ctagOf("/c/Work Cal/"); got != "2" {
		t.Errorf("the ctag on record is %q after the refetch", got)
	}

	// A stale read is served as it is and checked behind the page.
	u.age()
	before := srv.count("PROPFIND")
	report()
	for deadline := time.Now().Add(time.Second); srv.count("PROPFIND") == before; {
		if time.Now().After(deadline) {
			t.Fatalf("a stale read was not checked behind the page")
		}
		time.Sleep(time.Millisecond)
	}

	srv.ctag, srv.missing = "3", true
	u.age()
	u.refresh(context.Background())
	if n := len(u.entries); n != 0 {
		t.Errorf("a collection the server no longer has is still cached: %d entries", n)
	}
}

func TestFlightsLandAfterTheFetch(t *testing.T) {
	srv := &server{calls: map[string]int{}}
	c, _, _ := New(nil, DefaultPoll)
	rt := c.Transport("u", srv)
	for _, p := range []string{"/c/a/", "/c/b/", "/c/c/1.ics"} {
		req, _ := http.NewRequest("PROPFIND", "https://dav.example"+p, strings.NewReader("<q/>"))
		req.Header.Set("Depth", "1")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	u := c.user("u")
	if n := len(u.entries); n != 3 {
		t.Fatalf("expected 3 entries, got %d", n)
	}
	if n := len(u.flights); n != 0 {
		t.Errorf("%d flights kept after their requests landed", n)
	}
}

// TestStoreStartsWarm writes one account's entries out and reads them
// back into a new cache, which must answer the same query without
// asking the server, and learn its client from the first reader.
func TestStoreStartsWarm(t *testing.T) {
	var key fernet.Key
	if err := key.Generate(); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir(), &key)
	srv := &server{calls: map[string]int{}, ctag: "1"}
	c, warm, err := New(store, DefaultPoll)
	if err != nil || warm != 0 {
		t.Fatalf("a new store: %d accounts, %v", warm, err)
	}
	report := func(rt http.RoundTripper) {
		t.Helper()
		req, _ := http.NewRequest("REPORT", "https://dav.example/c/cal/", strings.NewReader("<q/>"))
		req.Header.Set("Depth", "1")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	report(c.Transport("u", srv))
	c.Stop()

	again, warm, err := New(store, DefaultPoll)
	if err != nil || warm != 1 {
		t.Fatalf("after a restart: %d accounts, %v", warm, err)
	}
	report(again.Transport("u", srv))
	if n := srv.count("REPORT"); n != 1 {
		t.Errorf("the restarted cache asked the server again: %d REPORTs", n)
	}
	if e := again.user("u").get(cacheKey("REPORT", "/c/cal/", "1", []byte("<q/>"))); e == nil || e.replay == nil {
		t.Errorf("the loaded entry did not learn its client")
	}
}
