package alborz_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.mehdix.org/alborz"
	"git.mehdix.org/alborz/auth"
	"git.mehdix.org/alborz/mail"
	"git.mehdix.org/alborz/pim"
	"git.mehdix.org/alborz/pim/calendar"
	"github.com/labstack/echo/v4"
)

func TestForceRefreshFetchesFeeds(t *testing.T) {
	var hits, conditional atomic.Int32
	var down atomic.Bool
	feed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nX-WR-CALNAME:Hols\r\n" +
			"BEGIN:VEVENT\r\nUID:feast\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;VALUE=DATE:" + time.Now().Format("20060102") +
			"\r\nSUMMARY:Feast\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	}))
	defer feed.Close()
	stub := &davStub{cals: map[string]davCal{}, refuse: map[string]bool{}, keep: map[string]bool{}, gone: map[string]bool{}}
	dav := httptest.NewServer(stub)
	defer dav.Close()
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	s, err := alborz.New(e, &alborz.Options{
		Upstreams: []string{"test.local=imap+insecure://" + startIMAP(t),
			"test.local=caldav+insecure://" + strings.TrimPrefix(dav.URL, "http://") + "/dav"},
		LoginKey: smokeKey, DataDir: t.TempDir(), CacheDir: t.TempDir(), Cache: true,
	}, mail.TemplateFuncs)
	if err != nil {
		t.Fatal(err)
	}
	s.Outgoing.Guarded = feed.Client().Transport
	auth.Mount(s)
	mail.Mount(s)
	st, err := pim.Mount(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := calendar.Mount(s, st); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go e.Server.Serve(ln)
	t.Cleanup(func() { e.Close() })
	base := "http://" + ln.Addr().String()
	c := login(t, base)

	resp := postForm(t, c, base+"/calendars/subscribe", url.Values{"address": {feed.URL + "/f.ics"}})
	if resp.StatusCode != http.StatusFound || hits.Load() != 1 {
		t.Fatalf("subscribe %s hits %d", resp.Status, hits.Load())
	}
	resp = postForm(t, c, base+"/calendar/refresh", url.Values{})
	t.Logf("refresh %s loc %s hits %d cond %d", resp.Status, resp.Header.Get("Location"), hits.Load(), conditional.Load())
	if hits.Load() != 2 || conditional.Load() != 1 {
		t.Fatal("no conditional fetch")
	}

	down.Store(true)
	postForm(t, c, base+"/calendar/refresh", url.Values{})
	if hits.Load() != 3 {
		t.Fatalf("hits %d after a failed refresh", hits.Load())
	}
	if !strings.Contains(get(t, c, base+"/calendar"), "Feast") {
		t.Fatal("a failed refresh dropped the stored feed")
	}
}
