package dav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.mehdix.org/alborz/pim/store"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/fernet/fernet-go"
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

func through(t *testing.T, s *fakeDAV) (*Mirror, *caldav.Client) {
	t.Helper()
	var key fernet.Key
	if err := key.Generate(); err != nil {
		t.Fatal(err)
	}
	m, err := OpenMirror(t.TempDir(), Kind{Name: "caldav", Poll: time.Hour, Holds: store.Calendar}, &key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	c, err := caldav.NewClient(&http.Client{Transport: m.Transport("u", s)}, "https://dav.test/")
	if err != nil {
		t.Fatal(err)
	}
	return m, c
}

func summaries(t *testing.T, c *caldav.Client) []string {
	t.Helper()
	objects, err := c.QueryCalendar(context.Background(), "/cal/", &caldav.CalendarQuery{
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
			if err := m.Refresh(context.Background(), "u", s); err != nil {
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
	if err := m.Refresh(context.Background(), "u", s); err != nil {
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
}
