package dav

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz/internal/seal"
	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// fakeDAV is one calendar at /cal/; tokens are its change log's length, and forget refuses them all.
type fakeDAV struct {
	mu             sync.Mutex
	objects        map[string]string
	etags          map[string]string
	log            []string
	noSync, forget bool
	tokens         []string
	requests       int
	down, gone     bool
}

func event(summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:" + summary +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T100000Z\r\nSUMMARY:" + summary +
		"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func (s *fakeDAV) set(name, data string) string {
	s.log = append(s.log, name)
	if data == "" {
		delete(s.objects, name)
		return ""
	}
	s.objects[name], s.etags[name] = data, "e"+strconv.Itoa(len(s.log))
	return s.etags[name]
}

func (s *fakeDAV) change(name, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set(name, data)
}

func (s *fakeDAV) member(name string, data bool) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<D:response><D:href>/cal/%s</D:href>`, name)
	if _, ok := s.objects[name]; !ok {
		b.WriteString(`<D:status>HTTP/1.1 404 Not Found</D:status></D:response>`)
		return b.String()
	}
	fmt.Fprintf(&b, `<D:propstat><D:prop><D:resourcetype/><D:getetag>"%s"</D:getetag>`, s.etags[name])
	if data {
		b.WriteString("<C:calendar-data>")
		xml.EscapeText(&b, []byte(s.objects[name]))
		b.WriteString("</C:calendar-data>")
	}
	b.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
	return b.String()
}

// element is a sync-token or href in any prefix a client writes it.
var element = regexp.MustCompile(`<(?:\w+:)?(sync-token|href)(?:\s[^>]*)?>([^<]*)<`)

func (s *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if s.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if s.gone {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		s.set(path.Base(r.URL.Path), "")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	var out []string
	token := ""
	switch {
	case r.Method == http.MethodPut:
		w.Header().Set("ETag", strconv.Quote(s.set(path.Base(r.URL.Path), body)))
		w.WriteHeader(http.StatusCreated)
		return
	case r.Method == "PROPFIND":
		out = append(out, fmt.Sprintf(`<D:response><D:href>/cal/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype><CS:getctag>c%d</CS:getctag></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, len(s.log)))
		for name := range s.objects {
			if r.Header.Get("Depth") == "1" {
				out = append(out, s.member(name, false))
			}
		}
	case strings.Contains(body, "sync-collection"):
		token = element.FindStringSubmatch(body)[2]
		s.tokens = append(s.tokens, token)
		if s.noSync || token != "" && s.forget {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		since, _ := strconv.Atoi(token)
		names := s.log[since:]
		if token == "" {
			names = nil
			for name := range s.objects {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range slices.Compact(names) {
			out = append(out, s.member(name, true))
		}
		token = strconv.Itoa(len(s.log))
	case strings.Contains(body, "calendar-multiget"):
		for _, m := range element.FindAllStringSubmatch(body, -1) {
			if m[1] == "href" {
				out = append(out, s.member(path.Base(m[2]), true))
			}
		}
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusMultiStatus)
	fmt.Fprintf(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">%s`, strings.Join(out, ""))
	if token != "" {
		fmt.Fprintf(w, "<D:sync-token>%s</D:sync-token>", token)
	}
	io.WriteString(w, "</D:multistatus>")
}

func (s *fakeDAV) RoundTrip(req *http.Request) (*http.Response, error) { return respond(s, req), nil }

func (s *fakeDAV) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

var someone = Principal{Account: "u", Name: "u"}

func through(t *testing.T, s *fakeDAV) (*Mirror, *caldav.Client) {
	t.Helper()
	var key [seal.KeySize]byte
	rand.Read(key[:])
	root, err := seal.ParseRoot(base64.StdEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatal(err)
	}
	m, err := OpenMirror(t.TempDir(), Kind{Name: "caldav", Poll: time.Hour, Holds: store.Calendar}, root, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	c, err := caldav.NewClient(&http.Client{Transport: m.Transport(someone, s)}, "https://dav.test/")
	if err != nil {
		t.Fatal(err)
	}
	return m, c
}

func summaries(t *testing.T, c *caldav.Client) []string {
	t.Helper()
	return summariesIn(t, c, "/cal/")
}

func summariesIn(t *testing.T, c *caldav.Client, coll string) []string {
	t.Helper()
	objects, err := c.QueryCalendar(context.Background(), coll, &caldav.CalendarQuery{
		// Partial, as the pages ask, so the server re-encodes what it sends.
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", Props: []string{"VERSION"},
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true, AllComps: true}}},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range objects {
		summary, err := o.Data.Events()[0].Props.Text(ical.PropSummary)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, summary)
	}
	slices.Sort(out)
	return out
}

func newFake() *fakeDAV {
	s := &fakeDAV{objects: map[string]string{}, etags: map[string]string{}}
	s.set("a.ics", event("a"))
	return s
}

func TestMirrorAnswersFromItsCopy(t *testing.T) {
	s := newFake()
	// The copy serves a VEVENT without DTSTAMP as sent.
	s.change("b.ics", strings.Replace(event("b"), "DTSTAMP:20260101T000000Z\r\n", "", 1))
	_, c := through(t, s)
	if got := summaries(t, c); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("first read: %v", got)
	}
	before := s.count()
	if got := summaries(t, c); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("second read: %v", got)
	}
	if n := s.count() - before; n != 0 {
		t.Errorf("a read of a held collection reached the server %d times", n)
	}
}

func TestMirrorFollowsTheServer(t *testing.T) {
	for _, noSync := range []bool{false, true} {
		t.Run(fmt.Sprintf("noSync=%v", noSync), func(t *testing.T) {
			s := newFake()
			s.noSync = noSync
			m, c := through(t, s)
			summaries(t, c)
			s.change("b.ics", event("b"))
			s.change("a.ics", "")
			if err := m.Refresh(context.Background(), someone, s); err != nil {
				t.Fatal(err)
			}
			if got := summaries(t, c); !slices.Equal(got, []string{"b"}) {
				t.Errorf("after the server changed: %v", got)
			}
			// Refused once, a server without sync is compared from then on; one with it resumes from its token.
			want := []string{"", "1"}
			if noSync {
				want = []string{""}
			}
			if !slices.Equal(s.tokens, want) {
				t.Errorf("sync tokens sent: %q, want %q", s.tokens, want)
			}
		})
	}
}

func TestMirrorResyncsWhenTheTokenIsRefused(t *testing.T) {
	s := newFake()
	m, c := through(t, s)
	summaries(t, c)
	s.forget = true
	s.change("b.ics", event("b"))
	if err := m.Refresh(context.Background(), someone, s); err != nil {
		t.Fatal(err)
	}
	if got := summaries(t, c); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("after a refused token: %v", got)
	}
	if want := []string{"", "1", ""}; !slices.Equal(s.tokens, want) {
		t.Errorf("sync tokens sent: %q, want %q", s.tokens, want)
	}
}

func TestMirrorShowsAnOwnWriteAtOnce(t *testing.T) {
	s := newFake()
	_, c := through(t, s)
	summaries(t, c)
	cal, err := ical.NewDecoder(strings.NewReader(event("mine"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutCalendarObject(context.Background(), "/cal/mine.ics", cal, nil); err != nil {
		t.Fatal(err)
	}
	before := s.count()
	if got := summaries(t, c); !slices.Equal(got, []string{"a", "mine"}) {
		t.Errorf("after an own write: %v", got)
	}
	if n := s.count() - before; n != 0 {
		t.Errorf("the read after an own write reached the server %d times", n)
	}
	if err := c.RemoveAll(context.Background(), "/cal/mine.ics"); err != nil {
		t.Fatal(err)
	}
	if got := summaries(t, c); !slices.Equal(got, []string{"a"}) {
		t.Errorf("after an own delete: %v", got)
	}
}

// byHost is the internet as far as a test goes: each host its server.
type byHost map[string]http.RoundTripper

func (h byHost) RoundTrip(req *http.Request) (*http.Response, error) {
	return h[req.URL.Host].RoundTrip(req)
}

// One path on two servers is two collections, so naming another server needs nothing forgotten (ADR 28, 59).
func TestMirrorKeepsTheOldServersCopyApartFromTheNew(t *testing.T) {
	old, named := newFake(), newFake()
	named.change("a.ics", "")
	named.change("n.ics", event("n"))
	m, _ := through(t, old)
	web := byHost{"old.test": old, "new.test": named}
	read := func(host string) []string {
		u := &url.URL{Scheme: "https", Host: host, Path: "/"}
		out := sourceRouter{sources: []Source{{ID: SourceOwn, URL: u, Named: true}}, trusted: web, named: web,
			sign: func(*http.Request, Source) error { return nil }}
		c, err := caldav.NewClient(&http.Client{Transport: m.Transport(someone, out)}, base.String())
		if err != nil {
			t.Fatal(err)
		}
		return summariesIn(t, c, marker(host)+"/cal/")
	}
	if got := read("old.test"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("the old server's collection: %v", got)
	}
	if got := read("new.test"); !slices.Equal(got, []string{"n"}) {
		t.Errorf("the new server's collection at the same path read %v, want its own", got)
	}
	if named.count() == 0 {
		t.Errorf("the new server was never asked")
	}
	if _, kept, err := m.held(someone.key(), marker("old.test")+"/cal/"); err != nil || !kept {
		t.Errorf("the old server's copy is gone (%v)", err)
	}
}

// byName answers each login with its own server, as one path on a server may hold another collection per user.
type byName map[string]http.RoundTripper

func (n byName) RoundTrip(req *http.Request) (*http.Response, error) {
	name, _, _ := req.BasicAuth()
	return n[name].RoundTrip(req)
}

// An account renamed on its server reads its new name's collection, and the old name's copy stays (ADR 53, 59).
func TestMirrorNeverServesOneNamesCopyToAnother(t *testing.T) {
	alice, bob := newFake(), newFake()
	bob.change("a.ics", "")
	bob.change("b.ics", event("b"))
	m, _ := through(t, alice)
	web := byName{"alice": alice, "bob": bob}
	u := &url.URL{Scheme: "https", Host: "dav.test", Path: "/"}
	read := func(name string) []string {
		out := sourceRouter{sources: []Source{{ID: SourceOwn, URL: u, Named: true}}, trusted: web, named: web,
			sign: func(req *http.Request, _ Source) error { req.SetBasicAuth(name, "secret"); return nil }}
		c, err := caldav.NewClient(&http.Client{Transport: m.Transport(Principal{Account: "u", Name: name}, out)}, base.String())
		if err != nil {
			t.Fatal(err)
		}
		return summariesIn(t, c, marker(u.Host)+"/cal/")
	}
	if got := read("alice"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("alice's collection: %v", got)
	}
	if got := read("bob"); !slices.Equal(got, []string{"b"}) {
		t.Errorf("bob was served %v, want his own", got)
	}
	if _, kept, err := m.held(Principal{Account: "u", Name: "alice"}.key(), marker(u.Host)+"/cal/"); err != nil || !kept {
		t.Errorf("alice's copy is gone (%v)", err)
	}
}

// A component's BEGIN line may be folded or in any case (RFC 5545 3.1); the copy must not skip such an object.
func TestCopyParsesWhatMayHoldTheComponent(t *testing.T) {
	events := []caldav.CompFilter{{Name: "VEVENT"}}
	for _, c := range []struct {
		name    string
		raw     string
		filters []caldav.CompFilter
		want    bool
	}{
		{"plain", event("a"), events, true},
		{"folded with a space", strings.Replace(event("a"), "BEGIN:VEVENT", "BEGIN:VEV\r\n ENT", 1), events, true},
		{"folded with a tab", strings.Replace(event("a"), "BEGIN:VEVENT", "BEGIN:\r\n\tVEVENT", 1), events, true},
		{"folded with a bare LF", strings.Replace(event("a"), "BEGIN:VEVENT", "BEG\n IN:VEVENT", 1), events, true},
		{"lower case", strings.Replace(event("a"), "BEGIN:VEVENT", "begin:vevent", 1), events, true},
		{"mixed case", strings.Replace(event("a"), "BEGIN:VEVENT", "Begin:vEvent", 1), events, true},
		{"a task", strings.ReplaceAll(event("a"), "VEVENT", "VTODO"), events, false},
		{"a task asked without events", strings.ReplaceAll(event("a"), "VEVENT", "VTODO"),
			[]caldav.CompFilter{{Name: "VEVENT", IsNotDefined: true}}, true},
	} {
		if _, err := ical.NewDecoder(strings.NewReader(c.raw)).Decode(); err != nil {
			t.Fatalf("%s is not iCalendar: %v", c.name, err)
		}
		if got := beginsAll([]byte(c.raw), c.filters); got != c.want {
			t.Errorf("%s: parsed %v, want %v", c.name, got, c.want)
		}
	}
}

// Past its budget the copy lets the least recently read collections go from memory, never from disk.
func TestMirrorHoldsOpenedCollectionsWithinItsBudget(t *testing.T) {
	m, _ := through(t, newFake())
	read := func(i int) []string {
		s := newFake()
		s.change("a.ics", "")
		s.change("s.ics", event(fmt.Sprintf("s%d", i)))
		c, err := caldav.NewClient(&http.Client{Transport: m.Transport(Principal{Account: "u", Name: strconv.Itoa(i)}, s)}, "https://dav.test/")
		if err != nil {
			t.Fatal(err)
		}
		return summaries(t, c)
	}
	held := func(i int) bool {
		m.hotMu.Lock()
		defer m.hotMu.Unlock()
		return m.hot[objectsKey(Principal{Account: "u", Name: strconv.Itoa(i)}.key(), "/cal/")] != nil
	}
	read(0)
	one := m.hotBytes
	if one == 0 {
		t.Fatal("a read collection is not held")
	}
	m.hotMax = 2*one + one/2
	for i := range 5 {
		if got, want := read(i), []string{fmt.Sprintf("s%d", i)}; !slices.Equal(got, want) {
			t.Fatalf("collection %d read %v, want %v", i, got, want)
		}
		if m.hotBytes > m.hotMax || !held(i) {
			t.Fatalf("after reading %d: %d bytes held of %d, it held: %v", i, m.hotBytes, m.hotMax, held(i))
		}
	}
	if len(m.hot) != 2 || !held(3) || held(2) {
		t.Errorf("held %d collections, 3 held %v, 2 held %v; want the two last read", len(m.hot), held(3), held(2))
	}
	if got := read(0); !slices.Equal(got, []string{"s0"}) {
		t.Errorf("a collection let go read %v", got)
	}
}

// A held copy gone stale while its server is down is served, and the server asked again only after a poll.
func TestMirrorWaitsOutAFailedSync(t *testing.T) {
	s := newFake()
	m, c := through(t, s)
	summaries(t, c)
	s.mu.Lock()
	s.down = true
	s.mu.Unlock()
	cal, err := ical.NewDecoder(strings.NewReader(event("mine"))).Decode()
	if err != nil {
		t.Fatal(err)
	}
	// A refused write leaves the copy stale.
	if _, err := c.PutCalendarObject(context.Background(), "/cal/mine.ics", cal, nil); err == nil {
		t.Fatal("the down server took a write")
	}
	before := s.count()
	for range 5 {
		if got := summaries(t, c); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("a stale copy read %v", got)
		}
		m.work.Wait()
	}
	if n := s.count() - before; n != 1 {
		t.Errorf("five reads of a stale copy asked the down server %d times, want once", n)
	}
}

func TestMirrorLetsGoOfACollectionItsServerLost(t *testing.T) {
	s := newFake()
	m, c := through(t, s)
	var told []string
	m.gone = func(account string) { told = append(told, account) }
	summaries(t, c)
	s.mu.Lock()
	s.gone = true
	s.mu.Unlock()
	if err := m.Refresh(context.Background(), someone, s); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(told, []string{someone.Account}) {
		t.Errorf("the listing was told of %v, want %v", told, []string{someone.Account})
	}
	if _, held, err := m.held(someone.key(), "/cal/"); err != nil || held {
		t.Errorf("the copy of a collection its server lost is still held (err %v)", err)
	}
}
